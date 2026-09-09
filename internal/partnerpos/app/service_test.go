package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/app"
	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
	"github.com/avito-kitchen/platform/internal/partnerpos/testsupport"
)

func newService(t *testing.T) (*app.Service, *testsupport.Platform) {
	t.Helper()
	pf := testsupport.NewPlatform()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(pf, domain.DefaultMenu(), logger, app.Config{
		StockSyncInterval: time.Hour,
		PollInterval:      time.Hour,
		KitchenStep:       time.Minute,
		OrderPageLimit:    50,
	})
	return svc, pf
}

func order(id, status string, age time.Duration) app.Order {
	return app.Order{
		ID: id, Status: status, TotalMinor: 61900,
		UpdatedAt: time.Now().UTC().Add(-age).Format(time.RFC3339),
		Items: []app.OrderItem{{
			MenuItemID: testsupport.MenuItemSellable, ExternalID: "POS-001",
			Name: "Борщ с говядиной", Qty: 1,
		}},
	}
}

// The till addresses its own positions by external id, while the platform
// speaks in menu item ids. Publication is what builds the mapping.
func TestPublishMenuLearnsThePlatformIdentifiers(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)

	before := svc.Menu()
	if before.Published {
		t.Fatal("the till starts unpublished")
	}
	for _, item := range before.Menu.Items {
		if item.MenuItemID != "" {
			t.Fatalf("an unpublished position must carry no platform id: %+v", item)
		}
	}

	result, err := svc.PublishMenu(context.Background())
	if err != nil {
		t.Fatalf("PublishMenu() = %v", err)
	}
	if result.Items != 3 || result.Matched != 3 {
		t.Fatalf("result = %+v, want three matched positions", result)
	}
	after := svc.Menu()
	if !after.Published || after.MenuVersion != pf.MenuVersion {
		t.Fatalf("state = %+v", after)
	}
	item, _ := after.Menu.Find("POS-001")
	if item.MenuItemID != testsupport.MenuItemSellable {
		t.Fatalf("mapping not learned: %+v", item)
	}
}

// Stock is a separate step: publishing a menu never creates units.
func TestSyncStockNeedsAPublishedMenu(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)

	if _, err := svc.SyncStock(context.Background()); !errors.Is(err, domain.ErrNotPublished) {
		t.Fatalf("SyncStock() = %v, want ErrNotPublished", err)
	}
	if len(pf.Stocks) != 0 {
		t.Fatal("nothing may be pushed before publication")
	}

	if _, err := svc.PublishMenu(context.Background()); err != nil {
		t.Fatalf("PublishMenu() = %v", err)
	}
	result, err := svc.SyncStock(context.Background())
	if err != nil {
		t.Fatalf("SyncStock() = %v", err)
	}
	if result.Updated != 3 || result.Skipped != 0 {
		t.Fatalf("result = %+v", result)
	}
	stock, ok := pf.Stock(testsupport.MenuItemSellable)
	if !ok || stock.Qty != 10 || stock.Stopped {
		t.Fatalf("stock = %+v", stock)
	}
}

// The stop list must travel as its own call and must not touch the quantity.
func TestSyncStockCarriesTheStopListSeparately(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	if _, err := svc.PublishMenu(context.Background()); err != nil {
		t.Fatalf("PublishMenu() = %v", err)
	}
	stopped := true
	if _, err := svc.PatchItem("POS-001", domain.ItemPatch{SalesStopped: &stopped}); err != nil {
		t.Fatalf("PatchItem() = %v", err)
	}
	if _, err := svc.SyncStock(context.Background()); err != nil {
		t.Fatalf("SyncStock() = %v", err)
	}
	stock, _ := pf.Stock(testsupport.MenuItemSellable)
	if !stock.Stopped || stock.Qty != 10 {
		t.Fatalf("stock = %+v, want the stop list set and the quantity kept", stock)
	}
}

func TestPatchItemValidatesAndReportsUnknownPositions(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)

	if _, err := svc.PatchItem("POS-001", domain.ItemPatch{}); err == nil {
		t.Fatal("an empty patch must be rejected")
	}
	zero := int64(0)
	if _, err := svc.PatchItem("POS-001", domain.ItemPatch{PriceMinor: &zero}); err == nil {
		t.Fatal("a zero price must be rejected")
	}
	price := int64(43000)
	if _, err := svc.PatchItem("NOPE", domain.ItemPatch{PriceMinor: &price}); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatalf("unknown position = %v, want ErrItemNotFound", err)
	}
	item, err := svc.PatchItem("POS-001", domain.ItemPatch{PriceMinor: &price})
	if err != nil || item.PriceMinor != price {
		t.Fatalf("PatchItem() = %+v, %v", item, err)
	}
}

func TestSetBehaviorValidates(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)

	if _, err := svc.SetBehavior(domain.Behavior{Mode: "NOPE"}); err == nil {
		t.Fatal("an unknown mode must be rejected")
	}
	b, err := svc.SetBehavior(domain.Behavior{
		Mode: domain.ModeAutoReject, ETAMinutes: 15, RejectReason: "нет продуктов",
	})
	if err != nil {
		t.Fatalf("SetBehavior() = %v", err)
	}
	if svc.Behavior().Mode != domain.ModeAutoReject || b.ETAMinutes != 15 {
		t.Fatalf("behaviour = %+v", svc.Behavior())
	}
}

// autoAccept переводит кассу в режим автоматического приёма: по умолчанию она
// в очередь не вмешивается.
func autoAccept(t *testing.T, svc *app.Service) {
	t.Helper()
	if _, err := svc.SetBehavior(domain.Behavior{
		Mode: domain.ModeAutoAccept, ETAMinutes: domain.DefaultBehavior().ETAMinutes,
		RejectReason: "нет", Kitchen: true,
	}); err != nil {
		t.Fatalf("SetBehavior() = %v", err)
	}
}

func TestPollAcceptsNewOrders(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	autoAccept(t, svc)
	pf.SetOrders(order("o-1", domain.StatusCreated, 0))

	result, err := svc.PollOrders(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PollOrders() = %v", err)
	}
	if result.Accepted != 1 || result.Seen != 1 {
		t.Fatalf("result = %+v", result)
	}
	if eta, ok := pf.Accepted["o-1"]; !ok || eta != domain.DefaultBehavior().ETAMinutes {
		t.Fatalf("accepted with eta %d, ok=%v", eta, ok)
	}

	// The platform now reports the order as confirmed, so a second poll does
	// not accept it again.
	again, err := svc.PollOrders(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("second PollOrders() = %v", err)
	}
	if again.Accepted != 0 {
		t.Fatalf("a repeated poll accepted the order twice: %+v", again)
	}
}

func TestPollRefusesWhenConfigured(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	if _, err := svc.SetBehavior(domain.Behavior{
		Mode: domain.ModeAutoReject, ETAMinutes: 10, RejectReason: "закончились продукты",
	}); err != nil {
		t.Fatalf("SetBehavior() = %v", err)
	}
	pf.SetOrders(order("o-2", domain.StatusCreated, 0))

	result, err := svc.PollOrders(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PollOrders() = %v", err)
	}
	if result.Rejected != 1 {
		t.Fatalf("result = %+v", result)
	}
	if reason := pf.Rejected["o-2"]; reason != "закончились продукты" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestPollLeavesOrdersAloneInManualMode(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	if _, err := svc.SetBehavior(domain.Behavior{
		Mode: domain.ModeManual, ETAMinutes: 10, RejectReason: "нет",
	}); err != nil {
		t.Fatalf("SetBehavior() = %v", err)
	}
	pf.SetOrders(order("o-3", domain.StatusCreated, 0))

	result, err := svc.PollOrders(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PollOrders() = %v", err)
	}
	if result.Held != 1 || len(pf.Accepted) != 0 || len(pf.Rejected) != 0 {
		t.Fatalf("result = %+v, accepted=%v rejected=%v", result, pf.Accepted, pf.Rejected)
	}
}

// Preparation advances one step per poll and only when the status is old
// enough, so the demonstration walks the whole ladder without the till keeping
// any state.
func TestPollWalksThePreparationLadderOneStepAtATime(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	autoAccept(t, svc)
	pf.SetOrders(order("o-4", domain.StatusConfirmed, 0))

	if result, err := svc.PollOrders(context.Background(), time.Now().UTC()); err != nil || result.Advanced != 0 {
		t.Fatalf("a fresh status must not advance: %+v, %v", result, err)
	}
	pf.SetOrders(order("o-4", domain.StatusConfirmed, 2*time.Minute))
	if result, err := svc.PollOrders(context.Background(), time.Now().UTC()); err != nil || result.Advanced != 1 {
		t.Fatalf("PollOrders() = %+v, %v", result, err)
	}
	if got := pf.Advanced["o-4"]; len(got) != 1 || got[0] != domain.StatusCooking {
		t.Fatalf("advanced = %v, want one step to COOKING", got)
	}
}

// One refused order must not stop the rest of the queue.
func TestPollContinuesAfterOneFailure(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	autoAccept(t, svc)
	pf.SetOrders(order("o-5", domain.StatusCreated, 0), order("o-6", domain.StatusCreated, 0))
	pf.DecideErr = app.Unavailable("order accept", errors.New("connection refused"))

	result, err := svc.PollOrders(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PollOrders() = %v", err)
	}
	if result.Seen != 2 || result.Held != 2 || result.Accepted != 0 {
		t.Fatalf("result = %+v, want both orders seen and left alone", result)
	}
}

func TestPollReportsAnUnavailablePlatform(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	pf.ListErr = app.Unavailable("order list queue", errors.New("connection refused"))

	if _, err := svc.PollOrders(context.Background(), time.Now().UTC()); !errors.Is(err, app.ErrPlatformUnavailable) {
		t.Fatalf("PollOrders() = %v, want ErrPlatformUnavailable", err)
	}
}

func TestPublishReportsAnUnavailablePlatform(t *testing.T) {
	t.Parallel()
	svc, pf := newService(t)
	pf.PublishErr = app.Unavailable("catalog publish menu", errors.New("connection refused"))

	if _, err := svc.PublishMenu(context.Background()); !errors.Is(err, app.ErrPlatformUnavailable) {
		t.Fatalf("PublishMenu() = %v, want ErrPlatformUnavailable", err)
	}
	if svc.Menu().Published {
		t.Fatal("a failed publication must not mark the menu as published")
	}
}

// Одна неудачная публикация не должна оставлять кассу неопубликованной
// навсегда: фоновый цикл повторяет её до успеха и сразу выталкивает остатки.
func TestRunStockSyncPublishesAndRecoversAfterAFailure(t *testing.T) {
	t.Parallel()
	pf := testsupport.NewPlatform()
	pf.SetPublishErr(app.Unavailable("catalog publish menu", errors.New("connection refused")))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(pf, domain.DefaultMenu(), logger, app.Config{
		StockSyncInterval: time.Hour,
		PollInterval:      5 * time.Millisecond,
		KitchenStep:       time.Minute,
		OrderPageLimit:    50,
		AutoPublish:       true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.RunStockSync(ctx) }()

	waitFor(t, 2*time.Second, func() bool { return pf.PublishAttempts() > 0 },
		"цикл ни разу не попытался опубликовать меню")
	if svc.Menu().Published {
		t.Fatal("неудачная публикация не должна помечать меню опубликованным")
	}

	pf.SetPublishErr(nil)
	waitFor(t, 5*time.Second, func() bool { return svc.Menu().Published },
		"цикл не повторил публикацию после восстановления платформы")
	waitFor(t, 5*time.Second, func() bool {
		stock, ok := pf.Stock(testsupport.MenuItemSellable)
		return ok && stock.Qty == 10
	}, "остатки не выставлены после публикации")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunStockSync() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("цикл не остановился по отмене")
	}
}

// Касса выталкивает только то, что изменила у себя: слепая перезапись раз в
// минуту откатывала бы изменения, сделанные через API платформы.
func TestSyncPassPushesNothingWithoutOwnChanges(t *testing.T) {
	t.Parallel()
	pf := testsupport.NewPlatform()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(pf, domain.DefaultMenu(), logger, app.Config{
		StockSyncInterval: 5 * time.Millisecond,
		PollInterval:      5 * time.Millisecond,
		KitchenStep:       time.Minute,
		OrderPageLimit:    50,
		AutoPublish:       true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.RunStockSync(ctx) }()
	waitFor(t, 2*time.Second, func() bool { return svc.Menu().Published },
		"касса не опубликовала меню")
	waitFor(t, 2*time.Second, func() bool {
		stock, ok := pf.Stock(testsupport.MenuItemSellable)
		return ok && stock.Qty == 10
	}, "касса не выставила остатки после публикации")

	// Заведение меняет остаток через API платформы, минуя кассу.
	if err := pf.SetStock(context.Background(), testsupport.MenuItemSellable, 3, "merchant"); err != nil {
		t.Fatalf("SetStock() = %v", err)
	}
	time.Sleep(50 * time.Millisecond) // несколько проходов цикла
	if stock, _ := pf.Stock(testsupport.MenuItemSellable); stock.Qty != 3 {
		t.Fatalf("остаток = %d, касса не должна откатывать чужие изменения", stock.Qty)
	}

	// А своё изменение касса доносит.
	qty := int32(7)
	if _, err := svc.PatchItem("POS-001", domain.ItemPatch{OnHandQty: &qty}); err != nil {
		t.Fatalf("PatchItem() = %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		stock, _ := pf.Stock(testsupport.MenuItemSellable)
		return stock.Qty == 7
	}, "касса не вытолкнула собственное изменение")

	cancel()
	<-done
}

// waitFor ждёт условия с ограниченным сроком: у фоновой работы нет момента,
// в который можно проверить результат синхронно.
func waitFor(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The loops must stop when the process is asked to stop.
func TestLoopsStopOnCancellation(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- svc.RunOrderPolling(ctx) }()
	go func() { done <- svc.RunStockSync(ctx) }()

	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("loop returned %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a loop did not stop after cancellation")
		}
	}
}
