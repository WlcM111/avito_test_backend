package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/internal/inventory/testsupport"
	"github.com/google/uuid"
)

func newService(t *testing.T) (*app.Service, *testsupport.StockRepo, *testsupport.MerchantDirectory) {
	t.Helper()
	stock := testsupport.NewStockRepo()
	dir := testsupport.NewMerchantDirectory()
	svc := app.NewService(stock, testsupport.NewReservationRepo(stock), dir, app.Config{
		DefaultReservationTTL: 30 * time.Minute,
		MaxReservationTTL:     time.Hour,
		MaxAvailabilityBatch:  5,
	})
	return svc, stock, dir
}

func ptr[T any](v T) *T { return &v }

func TestAuthenticate(t *testing.T) {
	t.Parallel()
	svc, _, dir := newService(t)
	ctx := context.Background()

	identity, err := svc.Authenticate(ctx, testsupport.DevKey)
	if err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
	if identity.MerchantID != testsupport.MerchantID {
		t.Fatalf("merchant id = %s", identity.MerchantID)
	}

	if _, err := svc.Authenticate(ctx, ""); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("empty key = %v, want ErrMerchantUnauthenticated", err)
	}
	if _, err := svc.Authenticate(ctx, "unknown"); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("unknown key = %v, want ErrMerchantUnauthenticated", err)
	}

	// A merchant disconnected by the operator keeps a technically valid key
	// but must not be able to change stock.
	dir.Keys["suspended"] = app.MerchantIdentity{MerchantID: uuid.New(), Active: false}
	if _, err := svc.Authenticate(ctx, "suspended"); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("suspended merchant = %v, want ErrMerchantUnauthenticated", err)
	}

	// An outage of the directory must not be reported as a bad key.
	dir.Err = app.ErrMerchantDirectoryUnavailable
	_, err = svc.Authenticate(ctx, testsupport.DevKey)
	if !errors.Is(err, app.ErrMerchantDirectoryUnavailable) {
		t.Fatalf("directory outage = %v, want ErrMerchantDirectoryUnavailable", err)
	}
	if errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatal("an outage must not be reported as an authentication failure")
	}
}

func TestGetAvailabilityReportsUntrackedItems(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	unknown := uuid.New()

	items, err := svc.GetAvailability(context.Background(), testsupport.MerchantID,
		[]uuid.UUID{testsupport.ItemSellable, testsupport.ItemEnabledEmpty, unknown})
	if err != nil {
		t.Fatalf("GetAvailability() = %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (untracked items are reported, not omitted)", len(items))
	}
	byID := map[uuid.UUID]app.AvailabilityItem{}
	for _, item := range items {
		byID[item.MenuItemID] = item
	}
	if got := byID[testsupport.ItemSellable]; !got.Tracked || got.Available != 10 {
		t.Fatalf("sellable item = %+v", got)
	}
	// An ENABLED card with zero units must report zero availability: enabling
	// sales confirms nothing.
	if got := byID[testsupport.ItemEnabledEmpty]; !got.Tracked || got.Available != 0 ||
		got.SalesState != domain.SalesEnabled {
		t.Fatalf("enabled but empty item = %+v", got)
	}
	if got := byID[unknown]; got.Tracked {
		t.Fatalf("unknown item reported as tracked: %+v", got)
	}
}

func TestGetAvailabilityValidation(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.GetAvailability(ctx, uuid.Nil, []uuid.UUID{uuid.New()}); err == nil {
		t.Fatal("empty merchant id accepted")
	}
	if _, err := svc.GetAvailability(ctx, testsupport.MerchantID, nil); err == nil {
		t.Fatal("empty item list accepted")
	}
	tooMany := make([]uuid.UUID, 6)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}
	if _, err := svc.GetAvailability(ctx, testsupport.MerchantID, tooMany); err == nil {
		t.Fatal("batch above the configured limit accepted")
	}

	// Duplicates are collapsed rather than rejected.
	items, err := svc.GetAvailability(ctx, testsupport.MerchantID,
		[]uuid.UUID{testsupport.ItemSellable, testsupport.ItemSellable})
	if err != nil {
		t.Fatalf("GetAvailability() = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
}

// Enabling or stopping sales must never touch a quantity (INV-S8).
func TestSetSalesStateDoesNotTouchQuantities(t *testing.T) {
	t.Parallel()
	svc, stock, _ := newService(t)
	ctx := context.Background()

	before, _ := stock.Snapshot(testsupport.ItemStopped)
	after, err := svc.SetSalesState(ctx, testsupport.MerchantID, testsupport.ItemStopped,
		domain.SalesEnabled, nil)
	if err != nil {
		t.Fatalf("SetSalesState() = %v", err)
	}
	if after.SalesState != domain.SalesEnabled {
		t.Fatalf("sales state = %s", after.SalesState)
	}
	if after.OnHandQty != before.OnHandQty || after.ReservedQty != before.ReservedQty {
		t.Fatalf("quantities changed: before %d/%d after %d/%d",
			before.OnHandQty, before.ReservedQty, after.OnHandQty, after.ReservedQty)
	}

	if _, err := svc.SetSalesState(ctx, testsupport.MerchantID, testsupport.ItemStopped,
		domain.SalesState("PAUSED"), nil); err == nil {
		t.Fatal("unknown sales state accepted")
	}
}

// A card created by enabling sales starts empty and is therefore not orderable.
func TestSetSalesStateCreatesEmptyCard(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	fresh := uuid.New()

	item, err := svc.SetSalesState(context.Background(), testsupport.MerchantID, fresh,
		domain.SalesEnabled, nil)
	if err != nil {
		t.Fatalf("SetSalesState() = %v", err)
	}
	if item.OnHandQty != 0 || item.Sellable() {
		t.Fatalf("new card = %+v, want zero units and not sellable", item)
	}
}

func TestSetOnHandValidation(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.SetOnHand(ctx, testsupport.MerchantID, testsupport.ItemSellable, -1, nil, ""); err == nil {
		t.Fatal("negative quantity accepted")
	}
	item, err := svc.SetOnHand(ctx, testsupport.MerchantID, testsupport.ItemSellable, 7, nil, "stocktake")
	if err != nil {
		t.Fatalf("SetOnHand() = %v", err)
	}
	if item.OnHandQty != 7 {
		t.Fatalf("on hand = %d, want 7", item.OnHandQty)
	}

	// Optimistic concurrency control: a stale version must be refused.
	if _, err := svc.SetOnHand(ctx, testsupport.MerchantID, testsupport.ItemSellable, 8,
		ptr(int64(1)), ""); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale version = %v, want ErrVersionConflict", err)
	}
}

func TestAdjustOnHandDeduplicates(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	ctx := context.Background()

	first, err := svc.AdjustOnHand(ctx, testsupport.MerchantID, testsupport.ItemSellable, 5, "op-1", "delivery")
	if err != nil {
		t.Fatalf("first adjust = %v", err)
	}
	if first.Duplicate || first.Item.OnHandQty != 15 {
		t.Fatalf("first adjust = %+v, want 15 units and no duplicate flag", first.Item)
	}

	repeat, err := svc.AdjustOnHand(ctx, testsupport.MerchantID, testsupport.ItemSellable, 5, "op-1", "delivery")
	if err != nil {
		t.Fatalf("repeat adjust = %v", err)
	}
	if !repeat.Duplicate || repeat.Item.OnHandQty != 15 {
		t.Fatalf("repeat adjust = %+v, want the stored result", repeat)
	}

	if _, err := svc.AdjustOnHand(ctx, testsupport.MerchantID, testsupport.ItemSellable, 5, "", ""); err == nil {
		t.Fatal("adjustment without an operation key accepted")
	}
}

func TestReserveCommitReleaseCycle(t *testing.T) {
	t.Parallel()
	svc, stock, _ := newService(t)
	ctx := context.Background()
	orderID := uuid.New()

	outcome, err := svc.Reserve(ctx, orderID, testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: 3}}, time.Minute)
	if err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	if outcome.AlreadyExisted || outcome.Reservation.State != domain.ReservationHeld {
		t.Fatalf("outcome = %+v", outcome)
	}
	item, _ := stock.Snapshot(testsupport.ItemSellable)
	if item.ReservedQty != 3 || item.OnHandQty != 10 || item.AvailableQty() != 7 {
		t.Fatalf("after reserve = %+v, want 10 on hand / 3 held", item)
	}

	// The repeat must not hold anything again.
	repeat, err := svc.Reserve(ctx, orderID, testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: 3}}, time.Minute)
	if err != nil {
		t.Fatalf("repeat Reserve() = %v", err)
	}
	if !repeat.AlreadyExisted {
		t.Fatal("repeat was not reported as already existing")
	}
	item, _ = stock.Snapshot(testsupport.ItemSellable)
	if item.ReservedQty != 3 {
		t.Fatalf("repeat held units again: reserved = %d", item.ReservedQty)
	}

	committed, err := svc.Commit(ctx, orderID)
	if err != nil {
		t.Fatalf("Commit() = %v", err)
	}
	if committed.AlreadyApplied || committed.State != domain.ReservationCommitted {
		t.Fatalf("commit outcome = %+v", committed)
	}
	item, _ = stock.Snapshot(testsupport.ItemSellable)
	if item.OnHandQty != 7 || item.ReservedQty != 0 {
		t.Fatalf("after commit = %+v, want 7 on hand / 0 held", item)
	}

	again, err := svc.Commit(ctx, orderID)
	if err != nil || !again.AlreadyApplied {
		t.Fatalf("repeat Commit() = %+v, %v", again, err)
	}
	if _, err := svc.Release(ctx, orderID, domain.ReleaseOrderCancelled); !errors.Is(err, domain.ErrReservationNotHeld) {
		t.Fatalf("release after commit = %v, want ErrReservationNotHeld", err)
	}
}

func TestReserveRejectsWithPerLineDetail(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	unknown := uuid.New()

	_, err := svc.Reserve(context.Background(), uuid.New(), testsupport.MerchantID,
		[]domain.ReserveLine{
			{MenuItemID: testsupport.ItemSellable, Qty: 1},
			{MenuItemID: testsupport.ItemEnabledEmpty, Qty: 1},
			{MenuItemID: testsupport.ItemStopped, Qty: 1},
			{MenuItemID: unknown, Qty: 1},
		}, time.Minute)

	rejected, ok := domain.AsReservationRejected(err)
	if !ok {
		t.Fatalf("Reserve() = %v, want *ReservationRejectedError", err)
	}
	problems := map[uuid.UUID]domain.LineProblem{}
	for _, l := range rejected.Lines {
		problems[l.MenuItemID] = l.Problem
	}
	want := map[uuid.UUID]domain.LineProblem{
		testsupport.ItemSellable:     domain.LineProblemNone,
		testsupport.ItemEnabledEmpty: domain.LineProblemInsufficientUnits,
		testsupport.ItemStopped:      domain.LineProblemSalesStopped,
		unknown:                      domain.LineProblemNotTracked,
	}
	for id, expected := range want {
		if problems[id] != expected {
			t.Fatalf("problem for %s = %s, want %s", id, problems[id], expected)
		}
	}
}

func TestReserveIsAllOrNothing(t *testing.T) {
	t.Parallel()
	svc, stock, _ := newService(t)

	_, err := svc.Reserve(context.Background(), uuid.New(), testsupport.MerchantID,
		[]domain.ReserveLine{
			{MenuItemID: testsupport.ItemSellable, Qty: 1},
			{MenuItemID: testsupport.ItemStopped, Qty: 1},
		}, time.Minute)
	if _, ok := domain.AsReservationRejected(err); !ok {
		t.Fatalf("Reserve() = %v, want a rejection", err)
	}
	item, _ := stock.Snapshot(testsupport.ItemSellable)
	if item.ReservedQty != 0 {
		t.Fatalf("units were held for a rejected reservation: reserved = %d", item.ReservedQty)
	}
}

func TestReserveTTLIsClamped(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)

	outcome, err := svc.Reserve(context.Background(), uuid.New(), testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: 1}}, 10*time.Hour)
	if err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	if until := time.Until(outcome.Reservation.ExpiresAt); until > time.Hour+time.Minute {
		t.Fatalf("ttl was not clamped to the maximum: expires in %s", until)
	}

	// A zero TTL falls back to the configured default rather than failing.
	outcome, err = svc.Reserve(context.Background(), uuid.New(), testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: 1}}, 0)
	if err != nil {
		t.Fatalf("Reserve() with zero ttl = %v", err)
	}
	if until := time.Until(outcome.Reservation.ExpiresAt); until < 25*time.Minute {
		t.Fatalf("default ttl was not applied: expires in %s", until)
	}
}

func TestReleaseValidatesReason(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	ctx := context.Background()
	orderID := uuid.New()

	if _, err := svc.Reserve(ctx, orderID, testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: 1}}, time.Minute); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	// The sweeper reason must not be accepted from a caller.
	if _, err := svc.Release(ctx, orderID, domain.ReleaseTTLExpired); err == nil {
		t.Fatal("TTL_EXPIRED accepted from a caller")
	}
	if _, err := svc.Release(ctx, orderID, domain.ReleaseOrderRejected); err != nil {
		t.Fatalf("Release() = %v", err)
	}
}

func TestReleaseExpiredReturnsUnits(t *testing.T) {
	t.Parallel()
	svc, stock, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.Reserve(ctx, uuid.New(), testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: 4}}, time.Millisecond); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	released, err := svc.ReleaseExpired(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ReleaseExpired() = %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	item, _ := stock.Snapshot(testsupport.ItemSellable)
	if item.ReservedQty != 0 || item.OnHandQty != 10 {
		t.Fatalf("after sweep = %+v, want 10 on hand / 0 held", item)
	}

	// A second pass has nothing left to do.
	if released, err = svc.ReleaseExpired(ctx, time.Now().UTC(), 10); err != nil || released != 0 {
		t.Fatalf("second sweep = %d, %v", released, err)
	}
}
