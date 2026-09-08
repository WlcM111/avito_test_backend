package app

import (
	"context"
	"fmt"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/google/uuid"
)

// Config bounds the use cases. The values come from the service configuration
// and are validated at start-up.
type Config struct {
	DefaultReservationTTL time.Duration
	MaxReservationTTL     time.Duration
	MaxAvailabilityBatch  int
}

// Service implements the use cases of inventory-service.
type Service struct {
	stock        StockRepository
	reservations ReservationRepository
	merchants    MerchantDirectory
	cfg          Config
}

// NewService wires the use cases to their ports.
func NewService(stock StockRepository, reservations ReservationRepository,
	merchants MerchantDirectory, cfg Config) *Service {
	if cfg.DefaultReservationTTL <= 0 {
		cfg.DefaultReservationTTL = 30 * time.Minute
	}
	if cfg.MaxReservationTTL <= 0 {
		cfg.MaxReservationTTL = 2 * time.Hour
	}
	if cfg.MaxAvailabilityBatch <= 0 {
		cfg.MaxAvailabilityBatch = domain.MaxAvailabilityBatch
	}
	return &Service{stock: stock, reservations: reservations, merchants: merchants, cfg: cfg}
}

// Authenticate resolves a merchant access key through merchant-service.
func (s *Service) Authenticate(ctx context.Context, apiKey string) (MerchantIdentity, error) {
	if apiKey == "" {
		return MerchantIdentity{}, ErrMerchantUnauthenticated
	}
	id, err := s.merchants.Authenticate(ctx, apiKey)
	if err != nil {
		return MerchantIdentity{}, err
	}
	// A merchant disconnected by the operator keeps a technically valid key but
	// must not change stock, so the check belongs here rather than in transport.
	if !id.Active {
		return MerchantIdentity{}, ErrMerchantUnauthenticated
	}
	return id, nil
}

// AvailabilityItem is the availability of one requested menu item. Items the
// merchant does not track are reported with Tracked == false rather than being
// omitted, so the caller can distinguish "no card" from "zero units".
type AvailabilityItem struct {
	MenuItemID  uuid.UUID
	SalesState  domain.SalesState
	OnHandQty   int32
	ReservedQty int32
	Available   int32
	Tracked     bool
	Version     int64
}

// GetAvailability reports the per-unit availability of the requested items.
func (s *Service) GetAvailability(ctx context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) ([]AvailabilityItem, error) {
	if merchantID == uuid.Nil {
		ve := &domain.ValidationError{}
		ve.Add("merchant_id", "is required")
		return nil, ve
	}
	unique := dedupeUUIDs(menuItemIDs)
	if len(unique) == 0 {
		ve := &domain.ValidationError{}
		ve.Add("menu_item_ids", "must contain at least one identifier")
		return nil, ve
	}
	if len(unique) > s.cfg.MaxAvailabilityBatch {
		ve := &domain.ValidationError{}
		ve.Add("menu_item_ids", fmt.Sprintf("must not contain more than %d identifiers",
			s.cfg.MaxAvailabilityBatch))
		return nil, ve
	}

	found, err := s.stock.GetMany(ctx, merchantID, unique)
	if err != nil {
		return nil, fmt.Errorf("get availability: %w", err)
	}
	byItem := make(map[uuid.UUID]domain.StockItem, len(found))
	for _, item := range found {
		byItem[item.MenuItemID] = item
	}

	out := make([]AvailabilityItem, 0, len(unique))
	for _, id := range unique {
		item, tracked := byItem[id]
		if !tracked {
			// An untracked item is not sellable: no card means no confirmed units.
			out = append(out, AvailabilityItem{
				MenuItemID: id,
				SalesState: domain.SalesStopped,
				Tracked:    false,
			})
			continue
		}
		out = append(out, AvailabilityItem{
			MenuItemID:  item.MenuItemID,
			SalesState:  item.SalesState,
			OnHandQty:   item.OnHandQty,
			ReservedQty: item.ReservedQty,
			Available:   item.AvailableQty(),
			Tracked:     true,
			Version:     item.Version,
		})
	}
	return out, nil
}

// StockPage is one page of a merchant's stock cards.
type StockPage struct {
	Items  []domain.StockItem
	Total  int
	Limit  int
	Offset int
}

// ListStock returns the merchant's stock cards.
func (s *Service) ListStock(ctx context.Context, merchantID uuid.UUID,
	limit, offset int) (StockPage, error) {
	items, total, err := s.stock.List(ctx, merchantID, limit, offset)
	if err != nil {
		return StockPage{}, fmt.Errorf("list stock: %w", err)
	}
	return StockPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// GetStockItem returns one stock card of the merchant.
func (s *Service) GetStockItem(ctx context.Context, merchantID, menuItemID uuid.UUID) (domain.StockItem, error) {
	return s.stock.Get(ctx, merchantID, menuItemID)
}

// SetOnHand stores an absolute on-hand value. The card is created when it does
// not exist. expectedVersion enables optimistic concurrency control.
func (s *Service) SetOnHand(ctx context.Context, merchantID, menuItemID uuid.UUID,
	qty int32, expectedVersion *int64, reason string) (domain.StockItem, error) {
	if err := domain.ValidateOnHand(qty); err != nil {
		return domain.StockItem{}, err
	}
	if len([]rune(reason)) > domain.MaxReasonLength {
		ve := &domain.ValidationError{}
		ve.Add("reason", "must not exceed 200 characters")
		return domain.StockItem{}, ve
	}
	return s.stock.SetOnHand(ctx, merchantID, menuItemID, qty, expectedVersion, reason)
}

// AdjustResult reports a relative change and whether it was a deduplicated repeat.
type AdjustResult struct {
	Item      domain.StockItem
	Duplicate bool
}

// AdjustOnHand applies a relative change exactly once per operation key.
func (s *Service) AdjustOnHand(ctx context.Context, merchantID, menuItemID uuid.UUID,
	delta int32, operationKey, reason string) (AdjustResult, error) {
	if err := domain.ValidateAdjustment(delta, operationKey, reason); err != nil {
		return AdjustResult{}, err
	}
	item, duplicate, err := s.stock.AdjustOnHand(ctx, merchantID, menuItemID, delta, operationKey, reason)
	if err != nil {
		return AdjustResult{}, err
	}
	return AdjustResult{Item: item, Duplicate: duplicate}, nil
}

// SetSalesState changes the stop-list switch. It never changes quantities:
// enabling sales does not confirm any units (INV-S8).
func (s *Service) SetSalesState(ctx context.Context, merchantID, menuItemID uuid.UUID,
	state domain.SalesState, expectedVersion *int64) (domain.StockItem, error) {
	if !state.Valid() {
		ve := &domain.ValidationError{}
		ve.Add("sales_state", "must be ENABLED or STOPPED")
		return domain.StockItem{}, ve
	}
	return s.stock.SetSalesState(ctx, merchantID, menuItemID, state, expectedVersion)
}

// MovementPage is one page of a card's journal.
type MovementPage struct {
	Items  []domain.Movement
	Total  int
	Limit  int
	Offset int
}

// ListMovements returns the journal of one card, newest entries first.
func (s *Service) ListMovements(ctx context.Context, merchantID, menuItemID uuid.UUID,
	limit, offset int) (MovementPage, error) {
	items, total, err := s.stock.ListMovements(ctx, merchantID, menuItemID, limit, offset)
	if err != nil {
		return MovementPage{}, err
	}
	return MovementPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// Reserve holds units for an order, all lines or none.
func (s *Service) Reserve(ctx context.Context, reservationID, merchantID uuid.UUID,
	lines []domain.ReserveLine, ttl time.Duration) (ReserveOutcome, error) {
	switch {
	case ttl <= 0:
		ttl = s.cfg.DefaultReservationTTL
	case ttl > s.cfg.MaxReservationTTL:
		ttl = s.cfg.MaxReservationTTL
	}
	req := domain.ReserveRequest{
		ReservationID: reservationID,
		MerchantID:    merchantID,
		Lines:         domain.NormalizeLines(lines),
		TTL:           ttl,
	}
	if err := req.Validate(); err != nil {
		return ReserveOutcome{}, err
	}
	return s.reservations.Reserve(ctx, req)
}

// Commit consumes the units held by a reservation.
func (s *Service) Commit(ctx context.Context, reservationID uuid.UUID) (TransitionOutcome, error) {
	if reservationID == uuid.Nil {
		ve := &domain.ValidationError{}
		ve.Add("reservation_id", "is required")
		return TransitionOutcome{}, ve
	}
	return s.reservations.Commit(ctx, reservationID)
}

// Release returns the units held by a reservation to availability.
func (s *Service) Release(ctx context.Context, reservationID uuid.UUID,
	reason domain.ReleaseReason) (TransitionOutcome, error) {
	ve := &domain.ValidationError{}
	if reservationID == uuid.Nil {
		ve.Add("reservation_id", "is required")
	}
	if !reason.Valid() {
		ve.Add("reason", "must be ORDER_REJECTED, ORDER_CANCELLED or ORDER_EXPIRED")
	}
	if err := ve.OrNil(); err != nil {
		return TransitionOutcome{}, err
	}
	return s.reservations.Release(ctx, reservationID, reason)
}

// GetReservation returns one hold with its lines.
func (s *Service) GetReservation(ctx context.Context, reservationID uuid.UUID) (domain.Reservation, error) {
	return s.reservations.Get(ctx, reservationID)
}

// ReleaseExpired is the sweeper entry point: it releases holds whose TTL has
// passed. It is the second line of defence behind the normal release path.
func (s *Service) ReleaseExpired(ctx context.Context, now time.Time, batch int) (int, error) {
	if batch <= 0 {
		batch = 100
	}
	return s.reservations.ReleaseExpired(ctx, now, batch)
}

func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
