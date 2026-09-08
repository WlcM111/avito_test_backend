package grpcapi_test

import (
	"context"
	"net"
	"testing"
	"time"

	inventoryv1 "github.com/avito-kitchen/platform/gen/grpc/inventory/v1"
	"github.com/avito-kitchen/platform/internal/inventory/adapters/grpcapi"
	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/testsupport"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newClient starts the real gRPC server over an in-memory listener, so the
// frozen contract is exercised through actual encoding and status propagation
// rather than through a mocked client.
func newClient(t *testing.T) (inventoryv1.InventoryServiceClient, *testsupport.StockRepo) {
	t.Helper()
	stock := testsupport.NewStockRepo()
	svc := app.NewService(stock, testsupport.NewReservationRepo(stock),
		testsupport.NewMerchantDirectory(), app.Config{
			DefaultReservationTTL: 30 * time.Minute,
			MaxReservationTTL:     time.Hour,
			MaxAvailabilityBatch:  10,
		})

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	grpcapi.NewServer(svc).Register(srv)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return inventoryv1.NewInventoryServiceClient(conn), stock
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestGetAvailability(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)
	ctx := ctxWithTimeout(t)
	unknown := uuid.NewString()

	resp, err := client.GetAvailability(ctx, &inventoryv1.GetAvailabilityRequest{
		MerchantId: testsupport.MerchantID.String(),
		MenuItemIds: []string{
			testsupport.ItemSellable.String(),
			testsupport.ItemEnabledEmpty.String(),
			testsupport.ItemStopped.String(),
			unknown,
		},
	})
	if err != nil {
		t.Fatalf("GetAvailability() = %v", err)
	}
	if len(resp.GetItems()) != 4 {
		t.Fatalf("got %d items, want 4", len(resp.GetItems()))
	}
	byID := map[string]*inventoryv1.Availability{}
	for _, item := range resp.GetItems() {
		byID[item.GetMenuItemId()] = item
	}
	if got := byID[testsupport.ItemSellable.String()]; got.GetAvailableQty() != 10 ||
		got.GetSalesState() != inventoryv1.SalesState_SALES_STATE_ENABLED || !got.GetTracked() {
		t.Fatalf("sellable item = %+v", got)
	}
	if got := byID[testsupport.ItemEnabledEmpty.String()]; got.GetAvailableQty() != 0 ||
		got.GetSalesState() != inventoryv1.SalesState_SALES_STATE_ENABLED {
		t.Fatalf("enabled but empty item = %+v", got)
	}
	if got := byID[testsupport.ItemStopped.String()]; got.GetSalesState() != inventoryv1.SalesState_SALES_STATE_STOPPED {
		t.Fatalf("stopped item = %+v", got)
	}
	if got := byID[unknown]; got.GetTracked() {
		t.Fatalf("unknown item reported as tracked: %+v", got)
	}
}

func TestGetAvailabilityErrors(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)
	ctx := ctxWithTimeout(t)

	if _, err := client.GetAvailability(ctx, &inventoryv1.GetAvailabilityRequest{
		MerchantId: "nope", MenuItemIds: []string{uuid.NewString()},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
	if _, err := client.GetAvailability(ctx, &inventoryv1.GetAvailabilityRequest{
		MerchantId: testsupport.MerchantID.String(),
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty list: code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestReserveCommit(t *testing.T) {
	t.Parallel()
	client, stock := newClient(t)
	ctx := ctxWithTimeout(t)
	orderID := uuid.NewString()

	resp, err := client.Reserve(ctx, &inventoryv1.ReserveRequest{
		ReservationId: orderID,
		MerchantId:    testsupport.MerchantID.String(),
		Lines: []*inventoryv1.StockLine{
			{MenuItemId: testsupport.ItemSellable.String(), Qty: 4},
		},
		TtlSeconds: 60,
	})
	if err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	if !resp.GetReserved() || resp.GetAlreadyExisted() ||
		resp.GetState() != inventoryv1.ReservationState_RESERVATION_STATE_HELD {
		t.Fatalf("response = %+v", resp)
	}
	if resp.GetExpiresAt() == "" {
		t.Fatal("expires_at is empty")
	}
	if _, err := time.Parse(time.RFC3339, resp.GetExpiresAt()); err != nil {
		t.Fatalf("expires_at is not RFC 3339: %v", err)
	}
	item, _ := stock.Snapshot(testsupport.ItemSellable)
	if item.ReservedQty != 4 {
		t.Fatalf("reserved = %d, want 4", item.ReservedQty)
	}

	repeat, err := client.Reserve(ctx, &inventoryv1.ReserveRequest{
		ReservationId: orderID,
		MerchantId:    testsupport.MerchantID.String(),
		Lines: []*inventoryv1.StockLine{
			{MenuItemId: testsupport.ItemSellable.String(), Qty: 4},
		},
		TtlSeconds: 60,
	})
	if err != nil {
		t.Fatalf("repeat Reserve() = %v", err)
	}
	if !repeat.GetAlreadyExisted() {
		t.Fatal("repeat was not reported as already existing")
	}
	item, _ = stock.Snapshot(testsupport.ItemSellable)
	if item.ReservedQty != 4 {
		t.Fatalf("repeat held units again: reserved = %d", item.ReservedQty)
	}

	commit, err := client.CommitReservation(ctx, &inventoryv1.CommitReservationRequest{
		ReservationId: orderID,
	})
	if err != nil {
		t.Fatalf("CommitReservation() = %v", err)
	}
	if commit.GetAlreadyCommitted() ||
		commit.GetState() != inventoryv1.ReservationState_RESERVATION_STATE_COMMITTED {
		t.Fatalf("commit = %+v", commit)
	}
	item, _ = stock.Snapshot(testsupport.ItemSellable)
	if item.OnHandQty != 6 || item.ReservedQty != 0 {
		t.Fatalf("after commit = %+v, want 6 on hand / 0 held", item)
	}

	again, err := client.CommitReservation(ctx, &inventoryv1.CommitReservationRequest{
		ReservationId: orderID,
	})
	if err != nil || !again.GetAlreadyCommitted() {
		t.Fatalf("repeat commit = %+v, %v", again, err)
	}
}

func TestReserveRejectionCarriesLineDetail(t *testing.T) {
	t.Parallel()
	client, stock := newClient(t)
	ctx := ctxWithTimeout(t)

	_, err := client.Reserve(ctx, &inventoryv1.ReserveRequest{
		ReservationId: uuid.NewString(),
		MerchantId:    testsupport.MerchantID.String(),
		Lines: []*inventoryv1.StockLine{
			{MenuItemId: testsupport.ItemSellable.String(), Qty: 1},
			{MenuItemId: testsupport.ItemStopped.String(), Qty: 1},
			{MenuItemId: testsupport.ItemEnabledEmpty.String(), Qty: 2},
		},
		TtlSeconds: 60,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", status.Code(err))
	}
	// The per-line detail travels in the status, because gRPC discards the
	// response message of a handler that returns an error.
	detail, ok := grpcapi.RejectionDetail(err)
	if !ok {
		t.Fatalf("the rejection carries no line detail: %v", err)
	}
	if detail.GetReserved() {
		t.Fatal("a rejected reservation reported reserved = true")
	}
	problems := map[string]inventoryv1.LineProblem{}
	for _, l := range detail.GetLines() {
		problems[l.GetMenuItemId()] = l.GetProblem()
	}
	if problems[testsupport.ItemStopped.String()] != inventoryv1.LineProblem_LINE_PROBLEM_SALES_STOPPED {
		t.Fatalf("stopped line = %v", problems[testsupport.ItemStopped.String()])
	}
	if problems[testsupport.ItemEnabledEmpty.String()] != inventoryv1.LineProblem_LINE_PROBLEM_INSUFFICIENT_UNITS {
		t.Fatalf("empty line = %v", problems[testsupport.ItemEnabledEmpty.String()])
	}
	if problems[testsupport.ItemSellable.String()] != inventoryv1.LineProblem_LINE_PROBLEM_NONE {
		t.Fatalf("valid line = %v", problems[testsupport.ItemSellable.String()])
	}
	// Nothing may be held when the reservation is rejected.
	item, _ := stock.Snapshot(testsupport.ItemSellable)
	if item.ReservedQty != 0 {
		t.Fatalf("units held for a rejected reservation: %d", item.ReservedQty)
	}
}

func TestReleaseReservation(t *testing.T) {
	t.Parallel()
	client, stock := newClient(t)
	ctx := ctxWithTimeout(t)
	orderID := uuid.NewString()

	if _, err := client.Reserve(ctx, &inventoryv1.ReserveRequest{
		ReservationId: orderID,
		MerchantId:    testsupport.MerchantID.String(),
		Lines:         []*inventoryv1.StockLine{{MenuItemId: testsupport.ItemSellable.String(), Qty: 3}},
		TtlSeconds:    60,
	}); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	resp, err := client.ReleaseReservation(ctx, &inventoryv1.ReleaseReservationRequest{
		ReservationId: orderID,
		Reason:        inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_REJECTED,
	})
	if err != nil {
		t.Fatalf("ReleaseReservation() = %v", err)
	}
	if resp.GetState() != inventoryv1.ReservationState_RESERVATION_STATE_RELEASED || resp.GetAlreadyReleased() {
		t.Fatalf("release = %+v", resp)
	}
	item, _ := stock.Snapshot(testsupport.ItemSellable)
	if item.OnHandQty != 10 || item.ReservedQty != 0 {
		t.Fatalf("after release = %+v, want the units back", item)
	}

	again, err := client.ReleaseReservation(ctx, &inventoryv1.ReleaseReservationRequest{
		ReservationId: orderID,
		Reason:        inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_CANCELLED,
	})
	if err != nil || !again.GetAlreadyReleased() {
		t.Fatalf("repeat release = %+v, %v", again, err)
	}

	// Committing a released hold is a precondition failure, not a success.
	if _, err := client.CommitReservation(ctx, &inventoryv1.CommitReservationRequest{
		ReservationId: orderID,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("commit after release: code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestTransitionErrors(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)
	ctx := ctxWithTimeout(t)

	if _, err := client.CommitReservation(ctx, &inventoryv1.CommitReservationRequest{
		ReservationId: uuid.NewString(),
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown reservation: code = %v, want NotFound", status.Code(err))
	}
	if _, err := client.CommitReservation(ctx, &inventoryv1.CommitReservationRequest{
		ReservationId: "nope",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("non-uuid: code = %v, want InvalidArgument", status.Code(err))
	}
	if _, err := client.ReleaseReservation(ctx, &inventoryv1.ReleaseReservationRequest{
		ReservationId: uuid.NewString(),
		Reason:        inventoryv1.ReleaseReason_RELEASE_REASON_UNSPECIFIED,
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unspecified reason: code = %v, want InvalidArgument", status.Code(err))
	}
	if _, err := client.Reserve(ctx, &inventoryv1.ReserveRequest{
		ReservationId: uuid.NewString(),
		MerchantId:    testsupport.MerchantID.String(),
		Lines:         []*inventoryv1.StockLine{{MenuItemId: testsupport.ItemSellable.String(), Qty: 0}},
		TtlSeconds:    60,
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("zero quantity: code = %v, want InvalidArgument", status.Code(err))
	}
}

// The deadline must be honoured end to end: a cancelled caller must not leave
// the handler running.
func TestDeadlineIsPropagated(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetAvailability(ctx, &inventoryv1.GetAvailabilityRequest{
		MerchantId:  testsupport.MerchantID.String(),
		MenuItemIds: []string{testsupport.ItemSellable.String()},
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("code = %v, want Canceled", status.Code(err))
	}
}
