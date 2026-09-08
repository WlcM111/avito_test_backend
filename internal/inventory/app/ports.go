// Package app holds the use cases of inventory-service. It depends on the
// domain and on the ports declared here; adapters implement the ports. It
// knows nothing about HTTP, gRPC or SQL.
package app

import (
	"context"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/google/uuid"
)

// StockRepository is the storage port for stock cards and their journal.
//
// The three merchant-facing writes create the card when it does not exist yet.
// A newly created card always starts at zero units, so creating it never
// confirms any quantity.
type StockRepository interface {
	// Get returns one card or domain.ErrStockItemNotFound.
	Get(ctx context.Context, merchantID, menuItemID uuid.UUID) (domain.StockItem, error)
	// List returns one page of the merchant's cards and the total count.
	List(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]domain.StockItem, int, error)
	// GetMany returns the cards that exist among menuItemIDs. Missing cards are
	// omitted; the caller reports them as not tracked.
	GetMany(ctx context.Context, merchantID uuid.UUID, menuItemIDs []uuid.UUID) ([]domain.StockItem, error)
	// SetOnHand stores an absolute on-hand value and records a SET movement.
	// expectedVersion, when not nil, makes the write conditional.
	// Returns domain.ErrBelowReserved when the value is below the held units.
	SetOnHand(ctx context.Context, merchantID, menuItemID uuid.UUID, qty int32,
		expectedVersion *int64, reason string) (domain.StockItem, error)
	// AdjustOnHand applies a relative change once per operationKey and records
	// an ADJUST movement. The second return value reports a deduplicated repeat.
	AdjustOnHand(ctx context.Context, merchantID, menuItemID uuid.UUID, delta int32,
		operationKey, reason string) (domain.StockItem, bool, error)
	// SetSalesState changes only the stop-list switch. It must not touch any
	// quantity column (INV-S8) and therefore records no movement.
	SetSalesState(ctx context.Context, merchantID, menuItemID uuid.UUID,
		state domain.SalesState, expectedVersion *int64) (domain.StockItem, error)
	// ListMovements returns one page of the journal of a card, newest first.
	ListMovements(ctx context.Context, merchantID, menuItemID uuid.UUID,
		limit, offset int) ([]domain.Movement, int, error)
}

// ReserveOutcome is the result of a successful hold.
type ReserveOutcome struct {
	Reservation    domain.Reservation
	AlreadyExisted bool
}

// TransitionOutcome is the result of a terminal transition of a hold.
type TransitionOutcome struct {
	State          domain.ReservationState
	AlreadyApplied bool
}

// ReservationRepository is the storage port for holds of units.
type ReservationRepository interface {
	// Reserve holds the requested units for every line atomically. It is
	// idempotent by reservation id: a repeat holds nothing again and reports
	// AlreadyExisted. When any line cannot be held it returns
	// *domain.ReservationRejectedError describing every requested line.
	Reserve(ctx context.Context, req domain.ReserveRequest) (ReserveOutcome, error)
	// Commit consumes the held units. Idempotent for an already committed
	// hold; returns domain.ErrReservationNotHeld for a released one.
	Commit(ctx context.Context, reservationID uuid.UUID) (TransitionOutcome, error)
	// Release returns the held units to availability. Idempotent for an
	// already released hold; returns domain.ErrReservationNotHeld for a
	// committed one.
	Release(ctx context.Context, reservationID uuid.UUID,
		reason domain.ReleaseReason) (TransitionOutcome, error)
	// Get returns one hold with its lines, or domain.ErrReservationNotFound.
	Get(ctx context.Context, reservationID uuid.UUID) (domain.Reservation, error)
	// ReleaseExpired releases up to batch holds whose TTL has passed and
	// returns how many were released. Several instances may call it
	// concurrently without duplicating work.
	ReleaseExpired(ctx context.Context, now time.Time, batch int) (int, error)
}

// MerchantIdentity is what the merchant directory tells us about the caller.
type MerchantIdentity struct {
	MerchantID uuid.UUID
	Active     bool
}

// MerchantDirectory is the port to merchant-service. inventory-service does
// not own merchant identity and must ask its owner on every merchant request.
type MerchantDirectory interface {
	// Authenticate resolves an access key to its merchant, or returns
	// ErrMerchantUnauthenticated. A dependency failure returns
	// ErrMerchantDirectoryUnavailable so that the transport can answer 503
	// rather than 401.
	Authenticate(ctx context.Context, apiKey string) (MerchantIdentity, error)
}
