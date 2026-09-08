package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/internal/order/testsupport"
	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

type env struct {
	svc       *app.Service
	orders    *testsupport.Orders
	directory *testsupport.Directory
	catalogue *testsupport.Catalogue
	ledger    *testsupport.Ledger
}

func newEnv(t *testing.T) env {
	t.Helper()
	orders := testsupport.NewOrders()
	directory := testsupport.NewDirectory()
	catalogue := testsupport.NewCatalogue()
	ledger := testsupport.NewLedger()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(orders, orders, directory, catalogue, ledger, logger, app.Config{
		ReservationTTL:      30 * time.Minute,
		MaxDeliveryAttempts: 3,
	})
	return env{svc: svc, orders: orders, directory: directory, catalogue: catalogue, ledger: ledger}
}

func draft(lines ...domain.Line) domain.Draft {
	return domain.Draft{
		MerchantID:      testsupport.MerchantID,
		CustomerID:      testsupport.CustomerID,
		Lines:           lines,
		CustomerName:    "Иван",
		CustomerPhone:   "+79990000001",
		DeliveryAddress: "Невский проспект, 1",
	}
}

func line(id uuid.UUID, qty int32) domain.Line { return domain.Line{MenuItemID: id, Qty: qty} }

// --------------------------------------------------------------- preview

func TestPreviewRecalculatesTheBasket(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	preview, err := e.svc.PreviewOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)))
	if err != nil {
		t.Fatalf("PreviewOrder() = %v", err)
	}
	wantItems := testsupport.PriceSellable * 2
	if preview.ItemsTotalMinor != wantItems {
		t.Fatalf("items total = %d, want %d", preview.ItemsTotalMinor, wantItems)
	}
	if preview.TotalMinor != wantItems+testsupport.DeliveryFeeMinor {
		t.Fatalf("total = %d", preview.TotalMinor)
	}
	if !preview.Orderable() {
		t.Fatalf("preview must be orderable: %+v", preview)
	}
	if preview.Lines[0].AvailableQty != 10 {
		t.Fatalf("available qty = %d, want 10", preview.Lines[0].AvailableQty)
	}
}

// The prices come from the catalogue and the units from the stock ledger; the
// preview must report the problem of every position rather than the first one.
func TestPreviewReportsEveryProblem(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	preview, err := e.svc.PreviewOrder(context.Background(), draft(
		line(testsupport.ItemSellable, 1),
		line(testsupport.ItemEnabledEmpty, 1),
		line(testsupport.ItemStopped, 1),
		line(testsupport.ItemUnknown, 1),
	))
	if err != nil {
		t.Fatalf("PreviewOrder() = %v", err)
	}
	got := map[uuid.UUID]domain.ItemProblem{}
	for _, l := range preview.Lines {
		got[l.MenuItemID] = l.Problem
	}
	want := map[uuid.UUID]domain.ItemProblem{
		testsupport.ItemSellable:     domain.ItemProblemNone,
		testsupport.ItemEnabledEmpty: domain.ItemProblemInsufficientUnits,
		testsupport.ItemStopped:      domain.ItemProblemSalesStopped,
		testsupport.ItemUnknown:      domain.ItemProblemNotFound,
	}
	for id, problem := range want {
		if got[id] != problem {
			t.Fatalf("problem of %s = %s, want %s", id, got[id], problem)
		}
	}
	if preview.Orderable() {
		t.Fatal("a basket with problems must not be orderable")
	}
	// A position that cannot be sold contributes nothing to the total.
	if preview.ItemsTotalMinor != testsupport.PriceSellable {
		t.Fatalf("items total = %d, want only the sellable position", preview.ItemsTotalMinor)
	}
}

func TestPreviewReportsOrderLevelProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*testsupport.Directory)
		problem domain.OrderProblem
	}{
		{
			name: "outlet paused intake",
			mutate: func(d *testsupport.Directory) {
				p := d.Profile(testsupport.MerchantID)
				p.IsAcceptingOrders = false
				d.SetProfile(p)
			},
			problem: domain.OrderProblemNotAcceptingOrders,
		},
		{
			name: "operator disconnected the outlet",
			mutate: func(d *testsupport.Directory) {
				p := d.Profile(testsupport.MerchantID)
				p.Active = false
				d.SetProfile(p)
			},
			problem: domain.OrderProblemMerchantSuspended,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			tc.mutate(e.directory)
			preview, err := e.svc.PreviewOrder(context.Background(),
				draft(line(testsupport.ItemSellable, 2)))
			if err != nil {
				t.Fatalf("PreviewOrder() = %v", err)
			}
			if len(preview.Problems) == 0 || preview.Problems[0] != tc.problem {
				t.Fatalf("problems = %+v, want %s", preview.Problems, tc.problem)
			}
			if preview.Orderable() {
				t.Fatal("the basket must not be orderable")
			}
		})
	}
}

// The minimum of the outlet applies to the positions, not to the delivery fee.
func TestPreviewMinimumIgnoresTheDeliveryFee(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	preview, err := e.svc.PreviewOrder(context.Background(), draft(line(testsupport.ItemSellable, 1)))
	if err != nil {
		t.Fatalf("PreviewOrder() = %v", err)
	}
	if testsupport.PriceSellable >= testsupport.MinOrderMinor {
		t.Fatalf("test premise broken: one position already reaches the minimum")
	}
	if preview.TotalMinor <= testsupport.MinOrderMinor {
		t.Fatalf("test premise broken: the total with delivery must exceed the minimum")
	}
	if len(preview.Problems) != 1 || preview.Problems[0] != domain.OrderProblemBelowMinOrder {
		t.Fatalf("problems = %+v, want MIN_ORDER_NOT_MET", preview.Problems)
	}
}

// Позиция с проблемой не даёт цены, поэтому сумма корзины перестаёт быть
// настоящей. Сообщать в этот момент о минимальной сумме — значит назвать
// следствие вместо причины: покупатель добавит позиций и всё равно получит
// отказ по стоп-листу.
func TestPreviewDoesNotBlameTheMinimumWhileAPositionIsBroken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	preview, err := e.svc.PreviewOrder(context.Background(), draft(
		line(testsupport.ItemStopped, 2),
		line(testsupport.ItemEnabledEmpty, 2),
	))
	if err != nil {
		t.Fatalf("PreviewOrder() = %v", err)
	}
	if preview.ItemsTotalMinor != 0 {
		t.Fatalf("items total = %d, want 0: ни одна позиция не заказуема", preview.ItemsTotalMinor)
	}
	for _, p := range preview.Problems {
		if p == domain.OrderProblemBelowMinOrder {
			t.Fatalf("problems = %+v, минимальная сумма не должна закрывать причину", preview.Problems)
		}
	}
	if preview.Orderable() {
		t.Fatal("корзина с проблемными позициями не заказуема")
	}
}

// Как только позиции исправны, минимальная сумма снова проверяется.
func TestPreviewReportsTheMinimumOnAHealthyBasket(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	preview, err := e.svc.PreviewOrder(context.Background(), draft(line(testsupport.ItemSellable, 1)))
	if err != nil {
		t.Fatalf("PreviewOrder() = %v", err)
	}
	if len(preview.Problems) != 1 || preview.Problems[0] != domain.OrderProblemBelowMinOrder {
		t.Fatalf("problems = %+v, want MIN_ORDER_NOT_MET", preview.Problems)
	}
}

func TestPreviewOfAnUnknownMerchant(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := draft(line(testsupport.ItemSellable, 1))
	d.MerchantID = uuid.New()
	if _, err := e.svc.PreviewOrder(context.Background(), d); !errors.Is(err, app.ErrMerchantNotFound) {
		t.Fatalf("PreviewOrder(unknown merchant) = %v, want ErrMerchantNotFound", err)
	}
}

func TestPreviewPropagatesADependencyOutage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.catalogue.SetErr(app.Unavailable("catalog-service", errors.New("connection refused")))
	_, err := e.svc.PreviewOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)))
	if !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("PreviewOrder() = %v, want ErrDependencyUnavailable", err)
	}
}

func TestPreviewValidatesTheBasket(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if _, err := e.svc.PreviewOrder(context.Background(), draft()); err == nil {
		t.Fatal("an empty basket must be rejected")
	}
}

// ---------------------------------------------------------------- create

func TestCreateOrderHoldsUnitsAndStoresTheSnapshot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	result, err := e.svc.CreateOrder(context.Background(),
		draft(line(testsupport.ItemSellable, 2)), "key-1")
	if err != nil {
		t.Fatalf("CreateOrder() = %v", err)
	}
	if result.Duplicate {
		t.Fatal("the first placement is not a duplicate")
	}
	order := result.Order
	if order.Status != domain.StatusCreated {
		t.Fatalf("status = %s, want CREATED", order.Status)
	}
	if order.TotalMinor != testsupport.PriceSellable*2+testsupport.DeliveryFeeMinor {
		t.Fatalf("total = %d", order.TotalMinor)
	}
	if len(order.Items) != 1 || order.Items[0].Name == "" || order.Items[0].UnitPriceMinor == 0 {
		t.Fatalf("the order must snapshot the name and the price: %+v", order.Items)
	}
	// The reservation identifier is the order identifier.
	if state, ok := e.ledger.HoldState(order.ID); !ok || state != "HELD" {
		t.Fatalf("hold state = %q, want HELD", state)
	}
	if _, reserved, _ := e.ledger.Snapshot(testsupport.ItemSellable); reserved != 2 {
		t.Fatalf("reserved = %d, want 2", reserved)
	}
}

// A repeat with the same key returns the first order and must not hold units a
// second time.
func TestCreateOrderIsIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first, err := e.svc.CreateOrder(context.Background(),
		draft(line(testsupport.ItemSellable, 2)), "key-1")
	if err != nil {
		t.Fatalf("first CreateOrder() = %v", err)
	}
	second, err := e.svc.CreateOrder(context.Background(),
		draft(line(testsupport.ItemSellable, 2)), "key-1")
	if err != nil {
		t.Fatalf("second CreateOrder() = %v", err)
	}
	if !second.Duplicate || second.Order.ID != first.Order.ID {
		t.Fatalf("a repeat must return the first order: %+v", second)
	}
	if _, reserved, _ := e.ledger.Snapshot(testsupport.ItemSellable); reserved != 2 {
		t.Fatalf("reserved = %d, want 2: the repeat must not hold units again", reserved)
	}
}

// The same key of a different customer is a different order.
func TestIdempotencyIsScopedToTheCustomer(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first, err := e.svc.CreateOrder(context.Background(),
		draft(line(testsupport.ItemSellable, 2)), "key-1")
	if err != nil {
		t.Fatalf("CreateOrder() = %v", err)
	}
	other := draft(line(testsupport.ItemSellable, 2))
	other.CustomerID = testsupport.OtherCustomerID
	second, err := e.svc.CreateOrder(context.Background(), other, "key-1")
	if err != nil {
		t.Fatalf("CreateOrder() = %v", err)
	}
	if second.Duplicate || second.Order.ID == first.Order.ID {
		t.Fatal("the key of another customer must produce a new order")
	}
}

func TestCreateOrderRequiresAnIdempotencyKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, err := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "  ")
	ve, ok := domain.AsValidationError(err)
	if !ok {
		t.Fatalf("CreateOrder() = %v, want a validation error", err)
	}
	found := false
	for _, v := range ve.Violations {
		if v.Field == "Idempotency-Key" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations %+v do not mention the missing key", ve.Violations)
	}
}

func TestCreateOrderRefusesAnUnorderableBasket(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, err := e.svc.CreateOrder(context.Background(),
		draft(line(testsupport.ItemSellable, 1), line(testsupport.ItemStopped, 1)), "key-1")
	notOrderable, ok := domain.AsNotOrderable(err)
	if !ok {
		t.Fatalf("CreateOrder() = %v, want a not-orderable error", err)
	}
	var stopped bool
	for _, l := range notOrderable.Lines {
		if l.MenuItemID == testsupport.ItemStopped && l.Problem == domain.ItemProblemSalesStopped {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("the stop list must be reported: %+v", notOrderable.Lines)
	}
	// Nothing was held for a refused basket.
	if _, reserved, _ := e.ledger.Snapshot(testsupport.ItemSellable); reserved != 0 {
		t.Fatalf("reserved = %d, want 0", reserved)
	}
}

// The price may change between the preview and the placement. The customer
// agreed to a total, and a different one aborts the order.
func TestCreateOrderRefusesAChangedTotal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := draft(line(testsupport.ItemSellable, 2))
	agreed := testsupport.PriceSellable*2 + testsupport.DeliveryFeeMinor
	d.ExpectedTotalMinor = ptr(agreed)

	e.catalogue.SetPrice(testsupport.ItemSellable, testsupport.PriceSellable+1000)
	if _, err := e.svc.CreateOrder(context.Background(), d, "key-1"); !errors.Is(err, domain.ErrPriceChanged) {
		t.Fatalf("CreateOrder() = %v, want ErrPriceChanged", err)
	}
	if _, reserved, _ := e.ledger.Snapshot(testsupport.ItemSellable); reserved != 0 {
		t.Fatalf("reserved = %d, want 0: a refused order holds nothing", reserved)
	}
}

func TestCreateOrderAcceptsAnUnchangedTotal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := draft(line(testsupport.ItemSellable, 2))
	d.ExpectedTotalMinor = ptr(testsupport.PriceSellable*2 + testsupport.DeliveryFeeMinor)
	if _, err := e.svc.CreateOrder(context.Background(), d, "key-1"); err != nil {
		t.Fatalf("CreateOrder() = %v", err)
	}
}

// The stock can run out between the preview and the hold. The refusal of
// inventory-service must reach the customer in the same shape as a refusal
// noticed during the preview.
func TestCreateOrderSurfacesALateShortage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.ledger.SetReserveErr(&app.ReserveRejectedError{Lines: []app.RejectedLine{{
		MenuItemID: testsupport.ItemSellable, RequestedQty: 2, AvailableQty: 1,
		Problem: domain.ItemProblemInsufficientUnits,
	}}})
	_, err := e.svc.CreateOrder(context.Background(),
		draft(line(testsupport.ItemSellable, 2)), "key-1")
	notOrderable, ok := domain.AsNotOrderable(err)
	if !ok {
		t.Fatalf("CreateOrder() = %v, want a not-orderable error", err)
	}
	if notOrderable.Lines[0].Problem != domain.ItemProblemInsufficientUnits ||
		notOrderable.Lines[0].AvailableQty != 1 {
		t.Fatalf("the shortage must carry the available quantity: %+v", notOrderable.Lines[0])
	}
}

// --------------------------------------------------------------- queries

func TestOrdersAreScopedToTheirOwners(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, err := e.svc.CreateOrder(context.Background(),
		draft(line(testsupport.ItemSellable, 2)), "key-1")
	if err != nil {
		t.Fatalf("CreateOrder() = %v", err)
	}
	orderID := created.Order.ID

	if _, err := e.svc.GetCustomerOrder(context.Background(), testsupport.CustomerID, orderID); err != nil {
		t.Fatalf("the owner must see the order: %v", err)
	}
	if _, err := e.svc.GetCustomerOrder(context.Background(), testsupport.OtherCustomerID, orderID); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("another customer must get not found, got %v", err)
	}
	if _, err := e.svc.GetMerchantOrder(context.Background(), testsupport.MerchantID, orderID); err != nil {
		t.Fatalf("the merchant must see the order: %v", err)
	}
	if _, err := e.svc.GetMerchantOrder(context.Background(), testsupport.OtherMerchantID, orderID); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("another merchant must get not found, got %v", err)
	}
}

func TestMerchantQueueFiltersByStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	if _, err := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-2"); err != nil {
		t.Fatalf("CreateOrder() = %v", err)
	}
	if _, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, first.Order.ID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}

	page, err := e.svc.ListMerchantOrders(context.Background(), testsupport.MerchantID,
		[]domain.Status{domain.StatusCreated}, 20, 0)
	if err != nil {
		t.Fatalf("ListMerchantOrders() = %v", err)
	}
	if page.Total != 1 || page.Items[0].Status != domain.StatusCreated {
		t.Fatalf("filtered queue = %+v", page)
	}

	all, err := e.svc.ListMerchantOrders(context.Background(), testsupport.MerchantID, nil, 20, 0)
	if err != nil {
		t.Fatalf("ListMerchantOrders() = %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("unfiltered queue total = %d, want 2", all.Total)
	}
}

func TestMerchantQueueRejectsAnUnknownStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, err := e.svc.ListMerchantOrders(context.Background(), testsupport.MerchantID,
		[]domain.Status{"SHIPPED"}, 20, 0)
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("ListMerchantOrders() = %v, want a validation error", err)
	}
}

func TestCustomerHistoryIsPaginated(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		if _, err := e.svc.CreateOrder(context.Background(),
			draft(line(testsupport.ItemSellable, 2)), "key-"+string(rune('a'+i))); err != nil {
			t.Fatalf("CreateOrder() = %v", err)
		}
	}
	page, err := e.svc.ListCustomerOrders(context.Background(), testsupport.CustomerID, 2, 0)
	if err != nil {
		t.Fatalf("ListCustomerOrders() = %v", err)
	}
	if page.Total != 3 || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}
}

// ------------------------------------------------------------ lifecycle

func TestAcceptSchedulesACommitAndIsIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	orderID := created.Order.ID

	result, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, orderID, 25)
	if err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}
	if result.Order.Status != domain.StatusConfirmed || result.Order.ETAMinutes != 25 {
		t.Fatalf("accepted order = %+v", result.Order)
	}
	messages := e.orders.MessagesOf(orderID)
	if len(messages) != 1 || messages[0].Type != domain.MessageCommitReservation {
		t.Fatalf("accepting must schedule exactly one commit: %+v", messages)
	}

	repeat, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, orderID, 25)
	if err != nil {
		t.Fatalf("repeated AcceptOrder() = %v", err)
	}
	if !repeat.AlreadyApplied {
		t.Fatal("a repeated confirmation must report that nothing changed")
	}
	if len(e.orders.MessagesOf(orderID)) != 1 {
		t.Fatal("a repeated confirmation must not schedule a second commit")
	}
}

func TestRejectSchedulesAReleaseAndKeepsTheReason(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")

	result, err := e.svc.RejectOrder(context.Background(), testsupport.MerchantID,
		created.Order.ID, "закончились продукты")
	if err != nil {
		t.Fatalf("RejectOrder() = %v", err)
	}
	if result.Order.Status != domain.StatusRejected || result.Order.RejectReason == "" {
		t.Fatalf("rejected order = %+v", result.Order)
	}
	messages := e.orders.MessagesOf(created.Order.ID)
	if len(messages) != 1 || messages[0].Reason != domain.ReleaseOrderRejected {
		t.Fatalf("refusal must schedule a release: %+v", messages)
	}
}

func TestRejectRequiresAReason(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	_, err := e.svc.RejectOrder(context.Background(), testsupport.MerchantID, created.Order.ID, "   ")
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("RejectOrder() = %v, want a validation error", err)
	}
}

func TestCancelSchedulesAReleaseAndOnlyBeforeTheDecision(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	orderID := created.Order.ID

	result, err := e.svc.CancelOrder(context.Background(), testsupport.CustomerID, orderID)
	if err != nil {
		t.Fatalf("CancelOrder() = %v", err)
	}
	if result.Order.Status != domain.StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", result.Order.Status)
	}
	messages := e.orders.MessagesOf(orderID)
	if len(messages) != 1 || messages[0].Reason != domain.ReleaseOrderCancelled {
		t.Fatalf("cancellation must schedule a release: %+v", messages)
	}

	// A cancelled order is terminal.
	_, err = e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, orderID, 20)
	conflict, ok := domain.AsStateConflict(err)
	if !ok || conflict.Current != domain.StatusCancelled {
		t.Fatalf("AcceptOrder(cancelled) = %v, want a state conflict", err)
	}
}

func TestCancelAfterConfirmationIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	if _, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, created.Order.ID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}
	_, err := e.svc.CancelOrder(context.Background(), testsupport.CustomerID, created.Order.ID)
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("CancelOrder(confirmed) = %v, want a state conflict", err)
	}
	if len(e.orders.MessagesOf(created.Order.ID)) != 1 {
		t.Fatal("a refused cancellation must not schedule a release")
	}
}

func TestCancelOfAnotherCustomerIsNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	_, err := e.svc.CancelOrder(context.Background(), testsupport.OtherCustomerID, created.Order.ID)
	if !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("CancelOrder(other customer) = %v, want ErrOrderNotFound", err)
	}
}

func TestProgressFollowsTheMatrix(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	orderID := created.Order.ID
	if _, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, orderID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}

	// Skipping a step is refused.
	if _, err := e.svc.AdvanceOrder(context.Background(), testsupport.MerchantID,
		orderID, domain.StatusDelivered); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("skipping preparation must be refused, got %v", err)
	}
	for _, target := range []domain.Status{
		domain.StatusCooking, domain.StatusDelivering, domain.StatusDelivered,
	} {
		result, err := e.svc.AdvanceOrder(context.Background(), testsupport.MerchantID, orderID, target)
		if err != nil {
			t.Fatalf("AdvanceOrder(%s) = %v", target, err)
		}
		if result.Order.Status != target {
			t.Fatalf("status = %s, want %s", result.Order.Status, target)
		}
	}
	// Progress schedules nothing: only the confirmation did.
	if len(e.orders.MessagesOf(orderID)) != 1 {
		t.Fatalf("progress must not schedule messages: %+v", e.orders.MessagesOf(orderID))
	}
}

func TestProgressRejectsADecisionStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	_, err := e.svc.AdvanceOrder(context.Background(), testsupport.MerchantID,
		created.Order.ID, domain.StatusConfirmed)
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("AdvanceOrder(CONFIRMED) = %v, want a validation error", err)
	}
}

func TestMerchantCannotTouchAnotherOutletsOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	_, err := e.svc.AcceptOrder(context.Background(), testsupport.OtherMerchantID, created.Order.ID, 20)
	if !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("AcceptOrder(other merchant) = %v, want ErrOrderNotFound", err)
	}
}

// ----------------------------------------------------------- dispatcher

func TestDispatchDeliversDecisionsOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	if _, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, created.Order.ID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}

	stats, err := e.svc.DispatchOutbox(context.Background(), time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("DispatchOutbox() = %v", err)
	}
	if stats.Claimed != 1 || stats.Sent != 1 {
		t.Fatalf("stats = %+v, want one delivered message", stats)
	}
	// The units are consumed, not merely unreserved.
	onHand, reserved, _ := e.ledger.Snapshot(testsupport.ItemSellable)
	if onHand != 8 || reserved != 0 {
		t.Fatalf("on hand = %d, reserved = %d, want 8 and 0", onHand, reserved)
	}

	// A second pass has nothing to do: the message is no longer pending.
	again, err := e.svc.DispatchOutbox(context.Background(), time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("second DispatchOutbox() = %v", err)
	}
	if again.Claimed != 0 {
		t.Fatalf("a delivered message must not be claimed again: %+v", again)
	}
	if e.ledger.Commits() != 1 {
		t.Fatalf("commits = %d, want exactly one business effect", e.ledger.Commits())
	}
}

func TestDispatchReturnsUnitsOfACancelledOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	if _, err := e.svc.CancelOrder(context.Background(), testsupport.CustomerID, created.Order.ID); err != nil {
		t.Fatalf("CancelOrder() = %v", err)
	}
	if _, err := e.svc.DispatchOutbox(context.Background(), time.Now().UTC(), 10, time.Minute); err != nil {
		t.Fatalf("DispatchOutbox() = %v", err)
	}
	onHand, reserved, _ := e.ledger.Snapshot(testsupport.ItemSellable)
	if onHand != 10 || reserved != 0 {
		t.Fatalf("on hand = %d, reserved = %d, want the units back", onHand, reserved)
	}
}

// A transient failure must be retried, not lost and not repeated immediately.
func TestDispatchRetriesATransientFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	if _, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, created.Order.ID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}
	e.ledger.SetCommitErr(app.Unavailable("inventory-service", errors.New("connection refused")))

	now := time.Now().UTC()
	stats, err := e.svc.DispatchOutbox(context.Background(), now, 10, time.Minute)
	if err != nil {
		t.Fatalf("DispatchOutbox() = %v", err)
	}
	if stats.Retried != 1 || stats.Sent != 0 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want one retry", stats)
	}
	messages := e.orders.MessagesOf(created.Order.ID)
	if messages[0].State != domain.MessagePending || messages[0].LastError == "" {
		t.Fatalf("the message must stay pending with a recorded error: %+v", messages[0])
	}
	if !messages[0].AvailableAt.After(now) {
		t.Fatalf("the next attempt must be in the future: %s", messages[0].AvailableAt)
	}

	// Once the dependency recovers, the decision lands.
	e.ledger.SetCommitErr(nil)
	later := messages[0].AvailableAt.Add(time.Second)
	if _, err := e.svc.DispatchOutbox(context.Background(), later, 10, time.Minute); err != nil {
		t.Fatalf("DispatchOutbox() = %v", err)
	}
	if e.ledger.Commits() != 1 {
		t.Fatalf("commits = %d, want 1", e.ledger.Commits())
	}
}

// A message the receiver calls malformed can never succeed, so it is parked
// immediately instead of consuming the whole retry budget.
func TestDispatchParksAnInvalidMessageImmediately(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	if _, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, created.Order.ID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}
	e.ledger.SetCommitErr(app.ErrInvalidRequest)

	stats, err := e.svc.DispatchOutbox(context.Background(), time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("DispatchOutbox() = %v", err)
	}
	if stats.Failed != 1 || stats.Retried != 0 {
		t.Fatalf("stats = %+v, want an immediate failure", stats)
	}
	if e.orders.MessagesOf(created.Order.ID)[0].State != domain.MessageFailed {
		t.Fatal("an invalid message must be parked")
	}
}

// After the configured number of attempts the message is parked for a human
// rather than retried forever.
func TestDispatchGivesUpAfterTheAttemptBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created, _ := e.svc.CreateOrder(context.Background(), draft(line(testsupport.ItemSellable, 2)), "key-1")
	if _, err := e.svc.AcceptOrder(context.Background(), testsupport.MerchantID, created.Order.ID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}
	e.ledger.SetCommitErr(app.Unavailable("inventory-service", errors.New("down")))

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if _, err := e.svc.DispatchOutbox(context.Background(), now, 10, time.Minute); err != nil {
			t.Fatalf("pass %d = %v", i, err)
		}
		now = now.Add(10 * time.Minute)
	}
	message := e.orders.MessagesOf(created.Order.ID)[0]
	if message.State != domain.MessageFailed {
		t.Fatalf("state = %s, want FAILED after the attempt budget", message.State)
	}
	if pending, _ := e.svc.PendingOutboxCount(context.Background()); pending != 0 {
		t.Fatalf("pending = %d, want 0", pending)
	}
}

// ------------------------------------------------------- authentication

func TestAuthenticate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	if _, err := e.svc.Authenticate(context.Background(), ""); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("empty key = %v", err)
	}
	if _, err := e.svc.Authenticate(context.Background(), "nope"); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("unknown key = %v", err)
	}
	identity, err := e.svc.Authenticate(context.Background(), testsupport.DevKey)
	if err != nil || identity.MerchantID != testsupport.MerchantID {
		t.Fatalf("Authenticate() = %+v, %v", identity, err)
	}

	e.directory.AddKey("suspended", app.MerchantIdentity{MerchantID: testsupport.MerchantID})
	if _, err := e.svc.Authenticate(context.Background(), "suspended"); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("a disconnected outlet must not be authenticated, got %v", err)
	}

	e.directory.SetAuthErr(app.Unavailable("merchant-service", errors.New("down")))
	if _, err := e.svc.Authenticate(context.Background(), testsupport.DevKey); !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("an outage must not be reported as a bad key, got %v", err)
	}
}
