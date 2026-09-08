// Package testsupport provides in-memory implementations of the inventory
// storage and directory ports. They exist so that the use-case and transport
// layers can be tested without a database or a running merchant-service. They
// are used by tests only; the SQL behaviour and the gRPC integration
// themselves are covered by the integration and contract tests.
package testsupport

import (
	"context"
	"sync"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/google/uuid"
)

// Fixed identifiers matching migrations/inventory/0002_seed.sql.
var (
	// MerchantID is the demonstration merchant "Кафе «Пример»".
	MerchantID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	// ItemSellable is ENABLED with units on hand.
	ItemSellable = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	// ItemEnabledEmpty is ENABLED with zero units: enabling sales confirms nothing.
	ItemEnabledEmpty = uuid.MustParse("a1111111-1111-4111-8111-222222222222")
	// ItemStopped is STOPPED although units are on hand.
	ItemStopped = uuid.MustParse("a1111111-1111-4111-8111-333333333333")
)

// DevKey is the development access key of the demonstration merchant.
const DevKey = "dev-merchant-key-kafe-primer-0001"

// StockRepo is an in-memory, domain-faithful StockRepository.
type StockRepo struct {
	mu         sync.Mutex
	items      map[uuid.UUID]domain.StockItem
	movements  map[uuid.UUID][]domain.Movement
	operations map[string]struct{}
	nextID     int64
}

// NewStockRepo seeds the repository with the three demonstration cards.
func NewStockRepo() *StockRepo {
	r := &StockRepo{
		items:      map[uuid.UUID]domain.StockItem{},
		movements:  map[uuid.UUID][]domain.Movement{},
		operations: map[string]struct{}{},
	}
	r.put(domain.StockItem{
		ID: uuid.New(), MerchantID: MerchantID, MenuItemID: ItemSellable,
		SalesState: domain.SalesEnabled, OnHandQty: 10, Version: 1,
	})
	r.put(domain.StockItem{
		ID: uuid.New(), MerchantID: MerchantID, MenuItemID: ItemEnabledEmpty,
		SalesState: domain.SalesEnabled, OnHandQty: 0, Version: 1,
	})
	r.put(domain.StockItem{
		ID: uuid.New(), MerchantID: MerchantID, MenuItemID: ItemStopped,
		SalesState: domain.SalesStopped, OnHandQty: 5, Version: 1,
	})
	return r
}

func (r *StockRepo) put(item domain.StockItem) {
	item.UpdatedAt = time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = item.UpdatedAt
	}
	r.items[item.MenuItemID] = item
}

// Snapshot returns the current state of one card, for assertions.
func (r *StockRepo) Snapshot(menuItemID uuid.UUID) (domain.StockItem, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[menuItemID]
	return item, ok
}

func (r *StockRepo) ensure(merchantID, menuItemID uuid.UUID) domain.StockItem {
	item, ok := r.items[menuItemID]
	if !ok {
		item = domain.StockItem{
			ID: uuid.New(), MerchantID: merchantID, MenuItemID: menuItemID,
			SalesState: domain.SalesEnabled, OnHandQty: 0, Version: 1,
		}
		r.put(item)
	}
	return item
}

// Get returns one card.
func (r *StockRepo) Get(_ context.Context, merchantID, menuItemID uuid.UUID) (domain.StockItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[menuItemID]
	if !ok || item.MerchantID != merchantID {
		return domain.StockItem{}, domain.ErrStockItemNotFound
	}
	return item, nil
}

// List returns every card of the merchant.
func (r *StockRepo) List(_ context.Context, merchantID uuid.UUID, limit, offset int) ([]domain.StockItem, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []domain.StockItem
	for _, item := range r.items {
		if item.MerchantID == merchantID {
			all = append(all, item)
		}
	}
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	end := min(offset+limit, total)
	return all[offset:end], total, nil
}

// GetMany returns the cards that exist among menuItemIDs.
func (r *StockRepo) GetMany(_ context.Context, merchantID uuid.UUID, ids []uuid.UUID) ([]domain.StockItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.StockItem
	for _, id := range ids {
		if item, ok := r.items[id]; ok && item.MerchantID == merchantID {
			out = append(out, item)
		}
	}
	return out, nil
}

// SetOnHand stores an absolute value.
func (r *StockRepo) SetOnHand(_ context.Context, merchantID, menuItemID uuid.UUID, qty int32,
	expectedVersion *int64, reason string) (domain.StockItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.ensure(merchantID, menuItemID)
	if expectedVersion != nil && *expectedVersion != item.Version {
		return domain.StockItem{}, domain.ErrVersionConflict
	}
	if qty < item.ReservedQty {
		return domain.StockItem{}, domain.ErrBelowReserved
	}
	delta := qty - item.OnHandQty
	item.OnHandQty = qty
	item.Version++
	r.put(item)
	r.record(item, domain.MovementSet, delta, 0, nil, "", reason)
	return item, nil
}

// AdjustOnHand applies a relative change once per operation key.
func (r *StockRepo) AdjustOnHand(_ context.Context, merchantID, menuItemID uuid.UUID, delta int32,
	operationKey, reason string) (domain.StockItem, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.ensure(merchantID, menuItemID)
	dedupeKey := item.ID.String() + "|" + operationKey
	if _, seen := r.operations[dedupeKey]; seen {
		return item, true, nil
	}
	next, err := item.ApplyAdjustment(delta)
	if err != nil {
		return domain.StockItem{}, false, err
	}
	r.operations[dedupeKey] = struct{}{}
	item.OnHandQty = next
	item.Version++
	r.put(item)
	r.record(item, domain.MovementAdjust, delta, 0, nil, operationKey, reason)
	return item, false, nil
}

// SetSalesState changes only the stop-list switch.
func (r *StockRepo) SetSalesState(_ context.Context, merchantID, menuItemID uuid.UUID,
	state domain.SalesState, expectedVersion *int64) (domain.StockItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.ensure(merchantID, menuItemID)
	if expectedVersion != nil && *expectedVersion != item.Version {
		return domain.StockItem{}, domain.ErrVersionConflict
	}
	item.SalesState = state
	item.Version++
	r.put(item)
	return item, nil
}

// ListMovements returns the journal of one card, newest first.
func (r *StockRepo) ListMovements(_ context.Context, merchantID, menuItemID uuid.UUID,
	limit, offset int) ([]domain.Movement, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[menuItemID]
	if !ok || item.MerchantID != merchantID {
		return nil, 0, domain.ErrStockItemNotFound
	}
	all := r.movements[item.ID]
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	end := min(offset+limit, total)
	return all[offset:end], total, nil
}

func (r *StockRepo) record(item domain.StockItem, kind domain.MovementKind,
	deltaOnHand, deltaReserved int32, reservationID *uuid.UUID, operationKey, reason string) {
	r.nextID++
	m := domain.Movement{
		ID: r.nextID, StockItemID: item.ID, MenuItemID: item.MenuItemID, Kind: kind,
		DeltaOnHand: deltaOnHand, DeltaReserved: deltaReserved,
		OnHandAfter: item.OnHandQty, ReservedAfter: item.ReservedQty,
		ReservationID: reservationID, OperationKey: operationKey, Reason: reason,
		CreatedAt: time.Now().UTC(),
	}
	r.movements[item.ID] = append([]domain.Movement{m}, r.movements[item.ID]...)
}

// ReservationRepo is an in-memory ReservationRepository sharing the stock repo.
type ReservationRepo struct {
	mu    sync.Mutex
	stock *StockRepo
	items map[uuid.UUID]domain.Reservation
}

// NewReservationRepo binds a reservation repository to a stock repository.
func NewReservationRepo(stock *StockRepo) *ReservationRepo {
	return &ReservationRepo{stock: stock, items: map[uuid.UUID]domain.Reservation{}}
}

// Reserve holds units all-or-nothing.
func (r *ReservationRepo) Reserve(_ context.Context, req domain.ReserveRequest) (app.ReserveOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.items[req.ReservationID]; ok {
		return app.ReserveOutcome{Reservation: existing, AlreadyExisted: true}, nil
	}

	r.stock.mu.Lock()
	defer r.stock.mu.Unlock()

	results := make([]domain.LineResult, 0, len(req.Lines))
	rejected := false
	for _, line := range req.Lines {
		item, ok := r.stock.items[line.MenuItemID]
		if !ok || item.MerchantID != req.MerchantID {
			results = append(results, domain.LineResult{
				MenuItemID: line.MenuItemID, RequestedQty: line.Qty,
				Problem: domain.LineProblemNotTracked,
			})
			rejected = true
			continue
		}
		problem := item.CanReserve(line.Qty)
		results = append(results, domain.LineResult{
			MenuItemID: line.MenuItemID, RequestedQty: line.Qty,
			AvailableQty: item.AvailableQty(), Problem: problem,
		})
		if problem != domain.LineProblemNone {
			rejected = true
		}
	}
	if rejected {
		return app.ReserveOutcome{}, &domain.ReservationRejectedError{Lines: results}
	}

	res := domain.Reservation{
		ID: req.ReservationID, MerchantID: req.MerchantID,
		State: domain.ReservationHeld, ExpiresAt: time.Now().UTC().Add(req.TTL),
	}
	for _, line := range req.Lines {
		item := r.stock.items[line.MenuItemID]
		item.ReservedQty += line.Qty
		item.Version++
		r.stock.put(item)
		r.stock.record(item, domain.MovementReserve, 0, line.Qty, &req.ReservationID, "", "")
		res.Lines = append(res.Lines, domain.ReservationLine{
			StockItemID: item.ID, MenuItemID: line.MenuItemID, Qty: line.Qty,
		})
	}
	r.items[req.ReservationID] = res
	return app.ReserveOutcome{Reservation: res}, nil
}

// Commit consumes the held units.
func (r *ReservationRepo) Commit(ctx context.Context, id uuid.UUID) (app.TransitionOutcome, error) {
	return r.finish(ctx, id, domain.ReservationCommitted, domain.MovementCommit, "")
}

// Release returns the held units to availability.
func (r *ReservationRepo) Release(ctx context.Context, id uuid.UUID,
	reason domain.ReleaseReason) (app.TransitionOutcome, error) {
	kind := domain.MovementRelease
	if reason == domain.ReleaseTTLExpired {
		kind = domain.MovementExpire
	}
	return r.finish(ctx, id, domain.ReservationReleased, kind, string(reason))
}

func (r *ReservationRepo) finish(_ context.Context, id uuid.UUID, target domain.ReservationState,
	kind domain.MovementKind, reason string) (app.TransitionOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.items[id]
	if !ok {
		return app.TransitionOutcome{}, domain.ErrReservationNotFound
	}
	if res.State == target {
		return app.TransitionOutcome{State: target, AlreadyApplied: true}, nil
	}
	if !res.State.CanTransitionTo(target) {
		return app.TransitionOutcome{}, domain.ErrReservationNotHeld
	}

	r.stock.mu.Lock()
	defer r.stock.mu.Unlock()
	for _, line := range res.Lines {
		item := r.stock.items[line.MenuItemID]
		var deltaOnHand int32
		if target == domain.ReservationCommitted {
			item.OnHandQty -= line.Qty
			deltaOnHand = -line.Qty
		}
		item.ReservedQty -= line.Qty
		item.Version++
		r.stock.put(item)
		r.stock.record(item, kind, deltaOnHand, -line.Qty, &id, "", reason)
	}
	res.State = target
	r.items[id] = res
	return app.TransitionOutcome{State: target}, nil
}

// Get returns one hold.
func (r *ReservationRepo) Get(_ context.Context, id uuid.UUID) (domain.Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.items[id]
	if !ok {
		return domain.Reservation{}, domain.ErrReservationNotFound
	}
	return res, nil
}

// ReleaseExpired releases holds whose TTL has passed.
func (r *ReservationRepo) ReleaseExpired(ctx context.Context, now time.Time, batch int) (int, error) {
	r.mu.Lock()
	var expired []uuid.UUID
	for id, res := range r.items {
		if res.State == domain.ReservationHeld && res.ExpiresAt.Before(now) {
			expired = append(expired, id)
			if len(expired) >= batch {
				break
			}
		}
	}
	r.mu.Unlock()

	released := 0
	for _, id := range expired {
		out, err := r.Release(ctx, id, domain.ReleaseTTLExpired)
		if err != nil {
			return released, err
		}
		if !out.AlreadyApplied {
			released++
		}
	}
	return released, nil
}

// MerchantDirectory is an in-memory MerchantDirectory.
type MerchantDirectory struct {
	Keys map[string]app.MerchantIdentity
	Err  error
}

// NewMerchantDirectory maps the development key to the demonstration merchant.
func NewMerchantDirectory() *MerchantDirectory {
	return &MerchantDirectory{Keys: map[string]app.MerchantIdentity{
		DevKey: {MerchantID: MerchantID, Active: true},
	}}
}

// Authenticate resolves an access key.
func (d *MerchantDirectory) Authenticate(_ context.Context, apiKey string) (app.MerchantIdentity, error) {
	if d.Err != nil {
		return app.MerchantIdentity{}, d.Err
	}
	identity, ok := d.Keys[apiKey]
	if !ok {
		return app.MerchantIdentity{}, app.ErrMerchantUnauthenticated
	}
	return identity, nil
}
