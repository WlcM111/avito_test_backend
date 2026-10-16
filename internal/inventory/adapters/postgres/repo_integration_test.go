package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	pgstore "github.com/avito-kitchen/platform/internal/inventory/adapters/postgres"
	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/avito-kitchen/platform/test/testenv"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixed identifiers of migrations/inventory/0002_seed.sql.
var (
	merchantID       = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	itemSellable     = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	itemEnabledEmpty = uuid.MustParse("a1111111-1111-4111-8111-222222222222")
	itemStopped      = uuid.MustParse("a1111111-1111-4111-8111-333333333333")
)

func ptr[T any](v T) *T { return &v }

type repos struct {
	pool         *pgxpool.Pool
	stock        *pgstore.StockRepository
	reservations *pgstore.ReservationRepository
}

func newRepos(t *testing.T) repos {
	t.Helper()
	pool := testenv.RequireDB(t, migrations.DirInventory)
	tx := pgxx.NewTxManager(pool)
	stock := pgstore.NewStockRepository(tx)
	return repos{pool: pool, stock: stock, reservations: pgstore.NewReservationRepository(tx, stock)}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestMigrationsAreIdempotent(t *testing.T) {
	pool := testenv.RequireDB(t, migrations.DirInventory)
	ctx := testCtx(t)

	before, err := testenv.MigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := testenv.ApplyAgain(ctx, pool, migrations.DirInventory); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after, err := testenv.MigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if before != after {
		t.Fatalf("version changed on re-apply: %d -> %d", before, after)
	}

	var cards int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stock_items`).Scan(&cards); err != nil {
		t.Fatalf("count stock items: %v", err)
	}
	if cards != 3 {
		t.Fatalf("seeded cards = %d, want 3 (the seed must not be applied twice)", cards)
	}
}

func TestSeedCoversTheThreeStates(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	sellable, err := r.stock.Get(ctx, merchantID, itemSellable)
	if err != nil {
		t.Fatalf("get sellable: %v", err)
	}
	if !sellable.Sellable() || sellable.AvailableQty() != 10 {
		t.Fatalf("sellable card = %+v", sellable)
	}
	// An ENABLED card with zero units must not be sellable.
	empty, err := r.stock.Get(ctx, merchantID, itemEnabledEmpty)
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if empty.SalesState != domain.SalesEnabled || empty.Sellable() {
		t.Fatalf("enabled but empty card = %+v", empty)
	}
	stopped, err := r.stock.Get(ctx, merchantID, itemStopped)
	if err != nil {
		t.Fatalf("get stopped: %v", err)
	}
	if stopped.Sellable() || stopped.OnHandQty != 5 {
		t.Fatalf("stopped card = %+v", stopped)
	}

	if _, err := r.stock.Get(ctx, merchantID, uuid.New()); !errors.Is(err, domain.ErrStockItemNotFound) {
		t.Fatalf("unknown card = %v, want ErrStockItemNotFound", err)
	}
	// A card of another merchant must not be visible.
	if _, err := r.stock.Get(ctx, uuid.New(), itemSellable); !errors.Is(err, domain.ErrStockItemNotFound) {
		t.Fatalf("foreign card = %v, want ErrStockItemNotFound", err)
	}
}

func TestSetOnHandCreatesCardAndRecordsMovement(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)
	fresh := uuid.New()

	item, err := r.stock.SetOnHand(ctx, merchantID, fresh, 7, nil, "first delivery")
	if err != nil {
		t.Fatalf("SetOnHand: %v", err)
	}
	if item.OnHandQty != 7 || item.ReservedQty != 0 || item.SalesState != domain.SalesEnabled {
		t.Fatalf("created card = %+v", item)
	}

	movements, total, err := r.stock.ListMovements(ctx, merchantID, fresh, 10, 0)
	if err != nil {
		t.Fatalf("ListMovements: %v", err)
	}
	if total != 1 || movements[0].Kind != domain.MovementSet ||
		movements[0].DeltaOnHand != 7 || movements[0].OnHandAfter != 7 {
		t.Fatalf("journal = %+v (total %d)", movements, total)
	}
	if movements[0].Reason != "first delivery" {
		t.Fatalf("reason = %q", movements[0].Reason)
	}
}

func TestSetOnHandVersionConflictAndBelowReserved(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	if _, err := r.stock.SetOnHand(ctx, merchantID, itemSellable, 12, ptr(int64(99)), ""); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale version = %v, want ErrVersionConflict", err)
	}

	orderID := uuid.New()
	if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: orderID, MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 6}}, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// Dropping below the held units must be refused, never clamped.
	if _, err := r.stock.SetOnHand(ctx, merchantID, itemSellable, 3, nil, ""); !errors.Is(err, domain.ErrBelowReserved) {
		t.Fatalf("below reserved = %v, want ErrBelowReserved", err)
	}
	item, err := r.stock.Get(ctx, merchantID, itemSellable)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if item.OnHandQty != 10 || item.ReservedQty != 6 {
		t.Fatalf("state changed on a refused write: %+v", item)
	}
	// Setting exactly to the held amount is allowed.
	if _, err := r.stock.SetOnHand(ctx, merchantID, itemSellable, 6, nil, ""); err != nil {
		t.Fatalf("set to the reserved amount: %v", err)
	}
}

func TestAdjustOnHandIsDeduplicated(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	first, dup, err := r.stock.AdjustOnHand(ctx, merchantID, itemSellable, 5, "delivery-42", "delivery")
	if err != nil || dup {
		t.Fatalf("first adjust = %+v, dup=%v, err=%v", first, dup, err)
	}
	if first.OnHandQty != 15 {
		t.Fatalf("on hand = %d, want 15", first.OnHandQty)
	}

	repeat, dup, err := r.stock.AdjustOnHand(ctx, merchantID, itemSellable, 5, "delivery-42", "delivery")
	if err != nil {
		t.Fatalf("repeat adjust: %v", err)
	}
	if !dup || repeat.OnHandQty != 15 {
		t.Fatalf("repeat applied the delta twice: dup=%v item=%+v", dup, repeat)
	}

	// A different key applies again.
	third, dup, err := r.stock.AdjustOnHand(ctx, merchantID, itemSellable, -3, "spoilage-1", "spoilage")
	if err != nil || dup || third.OnHandQty != 12 {
		t.Fatalf("second key = %+v, dup=%v, err=%v", third, dup, err)
	}

	if _, _, err := r.stock.AdjustOnHand(ctx, merchantID, itemSellable, -100, "writeoff-1", ""); !errors.Is(err, domain.ErrNegativeResult) {
		t.Fatalf("over-write-off = %v, want ErrNegativeResult", err)
	}
	// The refused adjustment must not have consumed its key or changed anything.
	item, _ := r.stock.Get(ctx, merchantID, itemSellable)
	if item.OnHandQty != 12 {
		t.Fatalf("state changed on a refused adjustment: %+v", item)
	}

	_, total, err := r.stock.ListMovements(ctx, merchantID, itemSellable, 20, 0)
	if err != nil {
		t.Fatalf("ListMovements: %v", err)
	}
	// One SET from the seed plus two applied adjustments.
	if total != 3 {
		t.Fatalf("journal entries = %d, want 3", total)
	}
}

// INV-S8: the stop-list switch must not touch any quantity.
func TestSetSalesStateDoesNotTouchQuantities(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: uuid.New(), MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 4}}, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	before, _ := r.stock.Get(ctx, merchantID, itemSellable)

	// Stopping sales with active holds is allowed: it only blocks new holds.
	after, err := r.stock.SetSalesState(ctx, merchantID, itemSellable, domain.SalesStopped, nil)
	if err != nil {
		t.Fatalf("SetSalesState: %v", err)
	}
	if after.OnHandQty != before.OnHandQty || after.ReservedQty != before.ReservedQty {
		t.Fatalf("quantities changed: %d/%d -> %d/%d",
			before.OnHandQty, before.ReservedQty, after.OnHandQty, after.ReservedQty)
	}
	if after.SalesState != domain.SalesStopped {
		t.Fatalf("sales state = %s", after.SalesState)
	}

	// Enabling sales again must not confirm any unit either.
	back, err := r.stock.SetSalesState(ctx, merchantID, itemSellable, domain.SalesEnabled, nil)
	if err != nil {
		t.Fatalf("SetSalesState back: %v", err)
	}
	if back.OnHandQty != before.OnHandQty || back.ReservedQty != before.ReservedQty {
		t.Fatalf("enabling sales changed quantities: %+v", back)
	}

	// No movement is recorded because no quantity changed.
	_, total, err := r.stock.ListMovements(ctx, merchantID, itemSellable, 20, 0)
	if err != nil {
		t.Fatalf("ListMovements: %v", err)
	}
	if total != 2 {
		t.Fatalf("journal entries = %d, want 2 (seed SET and the hold)", total)
	}
}

func TestSetSalesStateCreatesEmptyCard(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)
	fresh := uuid.New()

	item, err := r.stock.SetSalesState(ctx, merchantID, fresh, domain.SalesEnabled, nil)
	if err != nil {
		t.Fatalf("SetSalesState: %v", err)
	}
	if item.OnHandQty != 0 || item.Sellable() {
		t.Fatalf("new card = %+v, want zero units and not sellable", item)
	}
}

func TestReserveHoldsAndReportsProblems(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	outcome, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: uuid.New(), MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 3}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if outcome.AlreadyExisted || outcome.Reservation.State != domain.ReservationHeld {
		t.Fatalf("outcome = %+v", outcome)
	}
	item, _ := r.stock.Get(ctx, merchantID, itemSellable)
	if item.OnHandQty != 10 || item.ReservedQty != 3 {
		t.Fatalf("after hold = %+v", item)
	}

	unknown := uuid.New()
	_, err = r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: uuid.New(), MerchantID: merchantID,
		Lines: domain.NormalizeLines([]domain.ReserveLine{
			{MenuItemID: itemSellable, Qty: 1},
			{MenuItemID: itemStopped, Qty: 1},
			{MenuItemID: itemEnabledEmpty, Qty: 1},
			{MenuItemID: unknown, Qty: 1},
		}), TTL: time.Hour,
	})
	rejected, ok := domain.AsReservationRejected(err)
	if !ok {
		t.Fatalf("Reserve = %v, want a rejection", err)
	}
	problems := map[uuid.UUID]domain.LineProblem{}
	for _, l := range rejected.Lines {
		problems[l.MenuItemID] = l.Problem
	}
	want := map[uuid.UUID]domain.LineProblem{
		itemSellable:     domain.LineProblemNone,
		itemStopped:      domain.LineProblemSalesStopped,
		itemEnabledEmpty: domain.LineProblemInsufficientUnits,
		unknown:          domain.LineProblemNotTracked,
	}
	for id, expected := range want {
		if problems[id] != expected {
			t.Fatalf("problem for %s = %s, want %s", id, problems[id], expected)
		}
	}
	// Nothing extra may be held after a rejection.
	item, _ = r.stock.Get(ctx, merchantID, itemSellable)
	if item.ReservedQty != 3 {
		t.Fatalf("a rejected reservation changed the held units: %d", item.ReservedQty)
	}
}

func TestReserveIsIdempotent(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)
	orderID := uuid.New()
	req := domain.ReserveRequest{
		ReservationID: orderID, MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 4}}, TTL: time.Hour,
	}

	if _, err := r.reservations.Reserve(ctx, req); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	repeat, err := r.reservations.Reserve(ctx, req)
	if err != nil {
		t.Fatalf("repeat Reserve: %v", err)
	}
	if !repeat.AlreadyExisted {
		t.Fatal("repeat was not reported as already existing")
	}
	item, _ := r.stock.Get(ctx, merchantID, itemSellable)
	if item.ReservedQty != 4 {
		t.Fatalf("repeat held units again: reserved = %d, want 4", item.ReservedQty)
	}
	if len(repeat.Reservation.Lines) != 1 || repeat.Reservation.Lines[0].Qty != 4 {
		t.Fatalf("repeat returned different lines: %+v", repeat.Reservation.Lines)
	}
}

// The central concurrency guarantee: several holds competing for the last
// units must not oversell.
func TestReserveConcurrentLastUnits(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	// Ten units on hand, sixteen callers each asking for three: at most three
	// can succeed.
	const (
		workers = 16
		qty     = 3
	)
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		rejected  int
		other     []error
	)
	begin := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			_, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
				ReservationID: uuid.New(), MerchantID: merchantID,
				Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: qty}}, TTL: time.Hour,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				succeeded++
				return
			}
			if _, isRejected := domain.AsReservationRejected(err); isRejected {
				rejected++
				return
			}
			other = append(other, err)
		}()
	}
	close(begin)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if succeeded != 3 || rejected != workers-3 {
		t.Fatalf("succeeded=%d rejected=%d, want 3 and %d", succeeded, rejected, workers-3)
	}
	item, _ := r.stock.Get(ctx, merchantID, itemSellable)
	if item.ReservedQty != 9 || item.OnHandQty != 10 {
		t.Fatalf("final card = %+v, want 10 on hand / 9 held", item)
	}
	if item.AvailableQty() < 0 {
		t.Fatalf("oversold: available = %d", item.AvailableQty())
	}
}

// Deterministic line ordering must prevent deadlocks between multi-item holds.
func TestReserveNoDeadlockOnOppositeOrder(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	first, second := itemSellable, uuid.New()
	if _, err := r.stock.SetOnHand(ctx, merchantID, second, 100, nil, "setup"); err != nil {
		t.Fatalf("SetOnHand: %v", err)
	}
	if _, err := r.stock.SetOnHand(ctx, merchantID, first, 100, nil, "setup"); err != nil {
		t.Fatalf("SetOnHand: %v", err)
	}

	const rounds = 40
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i := range rounds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lines := []domain.ReserveLine{
				{MenuItemID: first, Qty: 1}, {MenuItemID: second, Qty: 1},
			}
			if i%2 == 1 {
				lines[0], lines[1] = lines[1], lines[0]
			}
			_, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
				ReservationID: uuid.New(), MerchantID: merchantID,
				Lines: domain.NormalizeLines(lines), TTL: time.Hour,
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("holds failed, a deadlock is the likely cause: %v", errs[0])
	}
}

func TestCommitAndReleaseAreIdempotent(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	committed := uuid.New()
	if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: committed, MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 4}}, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	out, err := r.reservations.Commit(ctx, committed)
	if err != nil || out.AlreadyApplied || out.State != domain.ReservationCommitted {
		t.Fatalf("Commit = %+v, %v", out, err)
	}
	item, _ := r.stock.Get(ctx, merchantID, itemSellable)
	if item.OnHandQty != 6 || item.ReservedQty != 0 {
		t.Fatalf("after commit = %+v, want 6 on hand / 0 held", item)
	}
	again, err := r.reservations.Commit(ctx, committed)
	if err != nil || !again.AlreadyApplied {
		t.Fatalf("repeat commit = %+v, %v", again, err)
	}
	if _, err := r.reservations.Release(ctx, committed, domain.ReleaseOrderCancelled); !errors.Is(err, domain.ErrReservationNotHeld) {
		t.Fatalf("release after commit = %v, want ErrReservationNotHeld", err)
	}

	released := uuid.New()
	if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: released, MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 2}}, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := r.reservations.Release(ctx, released, domain.ReleaseOrderRejected); err != nil {
		t.Fatalf("Release: %v", err)
	}
	item, _ = r.stock.Get(ctx, merchantID, itemSellable)
	if item.OnHandQty != 6 || item.ReservedQty != 0 {
		t.Fatalf("after release = %+v, want the units back", item)
	}
	repeat, err := r.reservations.Release(ctx, released, domain.ReleaseOrderRejected)
	if err != nil || !repeat.AlreadyApplied {
		t.Fatalf("repeat release = %+v, %v", repeat, err)
	}

	if _, err := r.reservations.Commit(ctx, uuid.New()); !errors.Is(err, domain.ErrReservationNotFound) {
		t.Fatalf("unknown reservation = %v, want ErrReservationNotFound", err)
	}
}

// Only one terminal transition may take effect when commit and release race.
func TestCommitReleaseRace(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	const rounds = 12
	for range rounds {
		orderID := uuid.New()
		if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
			ReservationID: orderID, MerchantID: merchantID,
			Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 1}}, TTL: time.Hour,
		}); err != nil {
			t.Fatalf("Reserve: %v", err)
		}

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			applied []domain.ReservationState
			conf    int
		)
		begin := make(chan struct{})
		record := func(out app.TransitionOutcome, err error) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && !out.AlreadyApplied:
				applied = append(applied, out.State)
			case err == nil:
			case errors.Is(err, domain.ErrReservationNotHeld):
				conf++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}
		wg.Add(2)
		go func() { defer wg.Done(); <-begin; record(r.reservations.Commit(ctx, orderID)) }()
		go func() {
			defer wg.Done()
			<-begin
			record(r.reservations.Release(ctx, orderID, domain.ReleaseOrderCancelled))
		}()
		close(begin)
		wg.Wait()

		if len(applied) != 1 {
			t.Fatalf("terminal transitions applied = %d, want exactly 1", len(applied))
		}
		res, err := r.reservations.Get(ctx, orderID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if res.State != applied[0] {
			t.Fatalf("stored state %s does not match the applied transition %s", res.State, applied[0])
		}
	}

	// After twelve rounds of one unit each, the counters must still balance.
	item, _ := r.stock.Get(ctx, merchantID, itemSellable)
	if item.ReservedQty != 0 {
		t.Fatalf("held units left behind: %d", item.ReservedQty)
	}
	if item.OnHandQty+int32(0) < 0 || item.OnHandQty > 10 {
		t.Fatalf("on hand out of range: %d", item.OnHandQty)
	}
}

func TestSweeperReleasesExpiredHolds(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	expired := uuid.New()
	if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: expired, MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 5}}, TTL: time.Millisecond,
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	fresh := uuid.New()
	if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: fresh, MerchantID: merchantID,
		Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 2}}, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	released, err := r.reservations.ReleaseExpired(ctx, time.Now().UTC(), 100)
	if err != nil {
		t.Fatalf("ReleaseExpired: %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1 (the fresh hold must survive)", released)
	}
	item, _ := r.stock.Get(ctx, merchantID, itemSellable)
	if item.ReservedQty != 2 || item.OnHandQty != 10 {
		t.Fatalf("after sweep = %+v, want 10 on hand / 2 held", item)
	}

	// The expiry is journalled with its own movement kind.
	movements, _, err := r.stock.ListMovements(ctx, merchantID, itemSellable, 10, 0)
	if err != nil {
		t.Fatalf("ListMovements: %v", err)
	}
	if movements[0].Kind != domain.MovementExpire || movements[0].DeltaReserved != -5 {
		t.Fatalf("newest movement = %+v, want an EXPIRE of 5 units", movements[0])
	}

	// A second pass has nothing left to do.
	if released, err = r.reservations.ReleaseExpired(ctx, time.Now().UTC(), 100); err != nil || released != 0 {
		t.Fatalf("second sweep = %d, %v", released, err)
	}
}

// The sweeper must never undo a commit that won the race.
func TestSweeperCommitRace(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	const rounds = 10
	for range rounds {
		orderID := uuid.New()
		if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
			ReservationID: orderID, MerchantID: merchantID,
			Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 1}}, TTL: time.Millisecond,
		}); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		time.Sleep(3 * time.Millisecond)

		var (
			wg          sync.WaitGroup
			mu          sync.Mutex
			commitOK    bool
			sweptCount  int
			commitErrIs error
		)
		begin := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-begin
			out, err := r.reservations.Commit(ctx, orderID)
			mu.Lock()
			defer mu.Unlock()
			if err == nil && !out.AlreadyApplied {
				commitOK = true
			}
			commitErrIs = err
		}()
		go func() {
			defer wg.Done()
			<-begin
			n, err := r.reservations.ReleaseExpired(ctx, time.Now().UTC(), 10)
			if err != nil {
				t.Errorf("ReleaseExpired: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			sweptCount = n
		}()
		close(begin)
		wg.Wait()

		if commitOK && sweptCount > 0 {
			t.Fatal("the hold was both committed and swept")
		}
		if !commitOK && commitErrIs != nil && !errors.Is(commitErrIs, domain.ErrReservationNotHeld) {
			t.Fatalf("unexpected commit error: %v", commitErrIs)
		}
		res, err := r.reservations.Get(ctx, orderID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !res.State.Terminal() {
			t.Fatalf("state = %s, want a terminal state", res.State)
		}
	}
}

// The schema is the last line of defence if a future caller bypasses the domain.
func TestCheckConstraintsProtectInvariants(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	item, err := r.stock.Get(ctx, merchantID, itemSellable)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	reservationID := uuid.New()
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO stock_reservations (id, merchant_id, state, expires_at)
		 VALUES ($1, $2, 'HELD', now() + interval '1 hour')`,
		reservationID, merchantID); err != nil {
		t.Fatalf("insert reservation: %v", err)
	}

	cases := map[string]string{
		"negative on hand":       `UPDATE stock_items SET on_hand_qty = -1 WHERE id = '` + item.ID.String() + `'`,
		"negative reserved":      `UPDATE stock_items SET reserved_qty = -1 WHERE id = '` + item.ID.String() + `'`,
		"reserved above on hand": `UPDATE stock_items SET reserved_qty = 999 WHERE id = '` + item.ID.String() + `'`,
		"unknown sales state":    `UPDATE stock_items SET sales_state = 'PAUSED' WHERE id = '` + item.ID.String() + `'`,
		"unknown reservation state": `UPDATE stock_reservations SET state = 'LOST' WHERE id = '` +
			reservationID.String() + `'`,
		"zero reservation line": `INSERT INTO stock_reservation_lines (reservation_id, stock_item_id, menu_item_id, qty)
		                          VALUES ('` + reservationID.String() + `', '` + item.ID.String() + `', '` +
			itemSellable.String() + `', 0)`,
		"duplicate card": `INSERT INTO stock_items (id, merchant_id, menu_item_id)
		                   VALUES ('` + uuid.NewString() + `', '` + merchantID.String() + `', '` +
			itemSellable.String() + `')`,
		"unknown movement kind": `INSERT INTO stock_movements (stock_item_id, kind, delta_on_hand,
		                            delta_reserved, on_hand_after, reserved_after)
		                          VALUES ('` + item.ID.String() + `', 'TRANSFER', 0, 0, 0, 0)`,
	}
	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := r.pool.Exec(ctx, stmt)
			if err == nil {
				t.Fatal("statement was accepted, want a constraint violation")
			}
			if !pgxx.IsCheckViolation(err) && !pgxx.IsUniqueViolation(err) {
				t.Fatalf("error = %v, want a check or unique violation", err)
			}
		})
	}
}

// The use cases must behave the same over real SQL as over the in-memory doubles.
func TestServiceOverRealStorage(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)
	svc := app.NewService(r.stock, r.reservations, nil, app.Config{
		DefaultReservationTTL: time.Hour,
		MaxReservationTTL:     2 * time.Hour,
		MaxAvailabilityBatch:  10,
	})

	items, err := svc.GetAvailability(ctx, merchantID,
		[]uuid.UUID{itemSellable, itemEnabledEmpty, itemStopped, uuid.New()})
	if err != nil {
		t.Fatalf("GetAvailability: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}

	orderID := uuid.New()
	if _, err := svc.Reserve(ctx, orderID, merchantID,
		[]domain.ReserveLine{{MenuItemID: itemSellable, Qty: 2}}, time.Hour); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := svc.Commit(ctx, orderID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	page, err := svc.ListStock(ctx, merchantID, 10, 0)
	if err != nil {
		t.Fatalf("ListStock: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}
}

// assertJournalReconciles checks INV-S9 on one card: every journal entry must
// continue exactly where the previous one ended, and the newest one must match
// the card. A delta computed from a stale read breaks the chain.
func assertJournalReconciles(t *testing.T, r repos, menuItemID uuid.UUID) {
	t.Helper()
	ctx := testCtx(t)
	card, err := r.stock.Get(ctx, merchantID, menuItemID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	movements, total, err := r.stock.ListMovements(ctx, merchantID, menuItemID, 10000, 0)
	if err != nil {
		t.Fatalf("ListMovements: %v", err)
	}
	if total == 0 || total != len(movements) {
		t.Fatalf("journal = %d entries of %d", len(movements), total)
	}
	// Newest first: each entry continues the one below it.
	for i := len(movements) - 1; i > 0; i-- {
		prev, next := movements[i], movements[i-1]
		if next.OnHandAfter != prev.OnHandAfter+next.DeltaOnHand ||
			next.ReservedAfter != prev.ReservedAfter+next.DeltaReserved {
			t.Fatalf("journal entry %d (%s %+d/%+d) does not continue %d/%d: it ends at %d/%d",
				next.ID, next.Kind, next.DeltaOnHand, next.DeltaReserved,
				prev.OnHandAfter, prev.ReservedAfter, next.OnHandAfter, next.ReservedAfter)
		}
	}
	if movements[0].OnHandAfter != card.OnHandQty || movements[0].ReservedAfter != card.ReservedQty {
		t.Fatalf("journal ends at %d/%d, card is %d/%d", movements[0].OnHandAfter,
			movements[0].ReservedAfter, card.OnHandQty, card.ReservedQty)
	}
}

// Concurrent relative changes must all apply: each one is computed from the
// card it updates, never from a value read before a concurrent change.
func TestAdjustOnHandConcurrentDeltasAreAllApplied(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	begin := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			_, _, err := r.stock.AdjustOnHand(ctx, merchantID, itemSellable, 1,
				fmt.Sprintf("delivery-%d", i), "delivery")
			errs <- err
		}(i)
	}
	close(begin)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AdjustOnHand: %v", err)
		}
	}

	item, err := r.stock.Get(ctx, merchantID, itemSellable)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if item.OnHandQty != 10+workers {
		t.Fatalf("on hand = %d, want %d: concurrent adjustments were lost", item.OnHandQty, 10+workers)
	}
	assertJournalReconciles(t, r, itemSellable)
}

// A merchant write racing the consumption of a confirmed order must neither
// bring the consumed units back nor leave a journal that does not add up.
func TestMerchantWritesDoNotUndoAConcurrentCommit(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	for round := range 20 {
		if _, err := r.stock.SetOnHand(ctx, merchantID, itemSellable, 50, nil, "setup"); err != nil {
			t.Fatalf("SetOnHand: %v", err)
		}
		orderID := uuid.New()
		if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
			ReservationID: orderID, MerchantID: merchantID,
			Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 5}}, TTL: time.Hour,
		}); err != nil {
			t.Fatalf("Reserve: %v", err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		begin := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-begin
			_, err := r.reservations.Commit(ctx, orderID)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-begin
			_, _, err := r.stock.AdjustOnHand(ctx, merchantID, itemSellable, 1,
				fmt.Sprintf("race-%d", round), "delivery")
			errs <- err
		}()
		close(begin)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}

		item, err := r.stock.Get(ctx, merchantID, itemSellable)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if item.OnHandQty != 50-5+1 || item.ReservedQty != 0 {
			t.Fatalf("round %d: card = %d on hand / %d held, want 46 / 0",
				round, item.OnHandQty, item.ReservedQty)
		}
	}
	assertJournalReconciles(t, r, itemSellable)
}

// An absolute write racing a commit may land before or after it, but its
// journal entry must record the change it actually made.
func TestSetOnHandJournalStaysExactUnderAConcurrentCommit(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	for round := range 20 {
		orderID := uuid.New()
		if _, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
			ReservationID: orderID, MerchantID: merchantID,
			Lines: []domain.ReserveLine{{MenuItemID: itemSellable, Qty: 2}}, TTL: time.Hour,
		}); err != nil {
			t.Fatalf("round %d: Reserve: %v", round, err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		begin := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-begin
			_, err := r.reservations.Commit(ctx, orderID)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-begin
			_, err := r.stock.SetOnHand(ctx, merchantID, itemSellable, 10, nil, "stocktake")
			errs <- err
		}()
		close(begin)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
	assertJournalReconciles(t, r, itemSellable)
}

// A rejected hold must not blame the lines it managed to hold before the
// failing one: they are rolled back and can be ordered.
func TestReserveRejectionCountsBackTheHeldLines(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	_, err := r.reservations.Reserve(ctx, domain.ReserveRequest{
		ReservationID: uuid.New(), MerchantID: merchantID,
		Lines: domain.NormalizeLines([]domain.ReserveLine{
			{MenuItemID: itemSellable, Qty: 6},
			{MenuItemID: itemStopped, Qty: 1},
		}), TTL: time.Hour,
	})
	rejected, ok := domain.AsReservationRejected(err)
	if !ok {
		t.Fatalf("Reserve = %v, want a rejection", err)
	}
	byItem := map[uuid.UUID]domain.LineResult{}
	for _, l := range rejected.Lines {
		byItem[l.MenuItemID] = l
	}
	if got := byItem[itemSellable]; got.Problem != domain.LineProblemNone || got.AvailableQty != 10 {
		t.Fatalf("held line reported as %+v, want NONE with 10 available", got)
	}
	if got := byItem[itemStopped]; got.Problem != domain.LineProblemSalesStopped {
		t.Fatalf("stopped line reported as %+v, want SALES_STOPPED", got)
	}
	item, err := r.stock.Get(ctx, merchantID, itemSellable)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if item.ReservedQty != 0 {
		t.Fatalf("a rejected hold kept %d units", item.ReservedQty)
	}
}

// A page past the end has no rows, yet the client still needs the total to
// page back.
func TestListsPastTheEndKeepTheTotal(t *testing.T) {
	r := newRepos(t)
	ctx := testCtx(t)

	items, total, err := r.stock.List(ctx, merchantID, 10, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 || total != 3 {
		t.Fatalf("stock past the end = %d items, total %d; want 0 and 3", len(items), total)
	}

	movements, total, err := r.stock.ListMovements(ctx, merchantID, itemSellable, 10, 100)
	if err != nil {
		t.Fatalf("ListMovements: %v", err)
	}
	if len(movements) != 0 || total != 1 {
		t.Fatalf("journal past the end = %d entries, total %d; want 0 and 1", len(movements), total)
	}
}
