package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/internal/inventory/jobs"
	"github.com/avito-kitchen/platform/internal/inventory/testsupport"
	"github.com/google/uuid"
)

// testLogger keeps the worker quiet in tests.
func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newSweeper(t *testing.T, interval time.Duration) (*jobs.ReservationSweeper, *app.Service, *testsupport.StockRepo) {
	t.Helper()
	stock := testsupport.NewStockRepo()
	reservations := testsupport.NewReservationRepo(stock)
	svc := app.NewService(stock, reservations, testsupport.NewMerchantDirectory(), app.Config{
		DefaultReservationTTL: time.Minute,
		MaxReservationTTL:     time.Hour,
	})
	logger := testLogger()
	return jobs.NewReservationSweeper(svc, logger, interval, 100, 5*time.Second), svc, stock
}

// hold takes units for ttl and returns the reservation identifier.
func hold(t *testing.T, svc *app.Service, ttl time.Duration, qty int32) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := svc.Reserve(context.Background(), id, testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: qty}}, ttl); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	return id
}

// A hold whose TTL has passed must return its units: it is the second line of
// defence behind the normal release path, and without it a caller that died
// between taking the hold and recording the order would withdraw those units
// from sale for good.
func TestSweepReleasesExpiredHolds(t *testing.T) {
	t.Parallel()
	sweeper, svc, stock := newSweeper(t, time.Hour)

	before, ok := stock.Snapshot(testsupport.ItemSellable)
	if !ok {
		t.Fatal("the seeded stock card is missing")
	}
	id := hold(t, svc, time.Millisecond, 2)

	held, _ := stock.Snapshot(testsupport.ItemSellable)
	if held.ReservedQty != before.ReservedQty+2 {
		t.Fatalf("reserved = %d, want %d", held.ReservedQty, before.ReservedQty+2)
	}

	// The TTL is in the past by the time the pass runs.
	time.Sleep(5 * time.Millisecond)
	released, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() = %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}

	after, _ := stock.Snapshot(testsupport.ItemSellable)
	if after.ReservedQty != before.ReservedQty {
		t.Fatalf("reserved = %d, want the units back at %d", after.ReservedQty, before.ReservedQty)
	}
	if after.OnHandQty != before.OnHandQty {
		t.Fatalf("on hand = %d, want it unchanged at %d", after.OnHandQty, before.OnHandQty)
	}

	reservation, err := svc.GetReservation(context.Background(), id)
	if err != nil {
		t.Fatalf("GetReservation() = %v", err)
	}
	if reservation.State != domain.ReservationReleased {
		t.Fatalf("state = %s, want RELEASED", reservation.State)
	}
}

// A hold that is still valid must not be touched: sweeping it would take units
// away from an order that is still waiting for the merchant.
func TestSweepLeavesLiveHoldsAlone(t *testing.T) {
	t.Parallel()
	sweeper, svc, stock := newSweeper(t, time.Hour)

	before, _ := stock.Snapshot(testsupport.ItemSellable)
	hold(t, svc, time.Hour, 3)

	released, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() = %v", err)
	}
	if released != 0 {
		t.Fatalf("released = %d, want 0", released)
	}
	after, _ := stock.Snapshot(testsupport.ItemSellable)
	if after.ReservedQty != before.ReservedQty+3 {
		t.Fatalf("reserved = %d, want the hold kept", after.ReservedQty)
	}
}

// The pass is idempotent: a second sweep of the same expired hold releases
// nothing and must not return the units twice.
func TestSweepIsIdempotent(t *testing.T) {
	t.Parallel()
	sweeper, svc, stock := newSweeper(t, time.Hour)

	before, _ := stock.Snapshot(testsupport.ItemSellable)
	hold(t, svc, time.Millisecond, 4)
	time.Sleep(5 * time.Millisecond)

	if released, err := sweeper.Sweep(context.Background()); err != nil || released != 1 {
		t.Fatalf("first Sweep() = %d, %v", released, err)
	}
	if released, err := sweeper.Sweep(context.Background()); err != nil || released != 0 {
		t.Fatalf("second Sweep() = %d, %v, want nothing left to do", released, err)
	}
	after, _ := stock.Snapshot(testsupport.ItemSellable)
	if after.ReservedQty != before.ReservedQty || after.OnHandQty != before.OnHandQty {
		t.Fatalf("stock = %+v, want it back exactly where it started: %+v", after, before)
	}
}

// A committed hold is not expired work: its units are already consumed, and
// releasing them would create stock out of nothing.
func TestSweepDoesNotTouchCommittedHolds(t *testing.T) {
	t.Parallel()
	sweeper, svc, stock := newSweeper(t, time.Hour)

	before, _ := stock.Snapshot(testsupport.ItemSellable)
	id := hold(t, svc, time.Millisecond, 2)
	if _, err := svc.Commit(context.Background(), id); err != nil {
		t.Fatalf("Commit() = %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	if released, err := sweeper.Sweep(context.Background()); err != nil || released != 0 {
		t.Fatalf("Sweep() = %d, %v, want a committed hold left alone", released, err)
	}
	after, _ := stock.Snapshot(testsupport.ItemSellable)
	if after.OnHandQty != before.OnHandQty-2 || after.ReservedQty != before.ReservedQty {
		t.Fatalf("stock = %+v, want the two units consumed once", after)
	}
}

// The worker must stop when the process is asked to stop.
func TestRunStopsOnCancellation(t *testing.T) {
	t.Parallel()
	sweeper, _, _ := newSweeper(t, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

// The worker keeps running: a pass releases expired holds on its own tick,
// which is what the sweeper exists for.
func TestRunReleasesOnItsOwnTick(t *testing.T) {
	t.Parallel()
	sweeper, svc, stock := newSweeper(t, 5*time.Millisecond)

	before, _ := stock.Snapshot(testsupport.ItemSellable)
	hold(t, svc, time.Millisecond, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if after, _ := stock.Snapshot(testsupport.ItemSellable); after.ReservedQty == before.ReservedQty {
			cancel()
			<-done
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the sweeper never released the expired hold on its own")
}
