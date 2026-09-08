// Package app holds the use cases of order-service. It depends on the domain
// and on the ports declared here; adapters implement the ports. It knows
// nothing about HTTP, gRPC or SQL.
package app

import (
	"context"
	"time"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
)

// CreateResult is the outcome of a placement.
type CreateResult struct {
	Order domain.Order
	// Duplicate reports that the Idempotency-Key had already been used by this
	// customer, so the returned order is the one created earlier.
	Duplicate bool
}

// TransitionCommand describes one lifecycle move of an order.
//
// The ownership expectations are part of the command rather than a separate
// read, because checking them in the same transaction as the update is what
// stops a concurrent change from slipping between the check and the write.
type TransitionCommand struct {
	OrderID uuid.UUID
	// ExpectMerchantID, when set, requires the order to belong to that
	// merchant; otherwise it is reported as not found.
	ExpectMerchantID *uuid.UUID
	// ExpectCustomerID, when set, requires the order to belong to that
	// customer.
	ExpectCustomerID *uuid.UUID
	Target           domain.Status
	ETAMinutes       int32
	RejectReason     string
	// Message is inserted in the same transaction as the status change. It is
	// nil for transitions that change nothing outside this service.
	Message *domain.Message
}

// TransitionResult is the outcome of a lifecycle move.
type TransitionResult struct {
	Order domain.Order
	// AlreadyApplied reports that the order was already in the target status,
	// so nothing changed and no message was scheduled. Repeating a merchant
	// decision is therefore safe without an idempotency header.
	AlreadyApplied bool
}

// OrderPage is one page of orders.
type OrderPage struct {
	Items  []domain.Order
	Total  int
	Limit  int
	Offset int
}

// OrderRepository is the storage port for orders and their positions.
type OrderRepository interface {
	// Create inserts the order and its positions in one transaction. A repeat
	// of the same (customer, idempotency key) does not create a second order:
	// the stored one is returned with duplicate = true.
	Create(ctx context.Context, order domain.Order) (stored domain.Order, duplicate bool, err error)
	// GetByID returns one order with its positions, or domain.ErrOrderNotFound.
	GetByID(ctx context.Context, orderID uuid.UUID) (domain.Order, error)
	// FindByIdempotencyKey returns the order a customer created with that key,
	// or domain.ErrOrderNotFound.
	FindByIdempotencyKey(ctx context.Context, customerID uuid.UUID, key string) (domain.Order, error)
	// ListByCustomer returns one page of the customer's history, newest first.
	ListByCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) (OrderPage, error)
	// ListByMerchant returns one page of the merchant's queue, oldest first.
	// An empty statuses slice means every status.
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, statuses []domain.Status,
		limit, offset int) (OrderPage, error)
	// Transition applies one lifecycle move and, in the same transaction,
	// inserts the outbox message the move requires. It returns
	// domain.ErrOrderNotFound when the order does not exist or does not belong
	// to the expected owner, and *domain.StateConflictError when the move is
	// not allowed from the current status.
	Transition(ctx context.Context, cmd TransitionCommand) (TransitionResult, error)
}

// OutboxRepository is the storage port of the transactional outbox.
type OutboxRepository interface {
	// Claim takes up to batch due messages, increments their attempt counter
	// and pushes their next visibility beyond the lease, so several dispatcher
	// instances never work on the same message. The claim commits before any
	// network call is made: a transaction must not stay open across gRPC.
	Claim(ctx context.Context, now time.Time, batch int, lease time.Duration) ([]domain.Message, error)
	// MarkSent records a delivered message.
	MarkSent(ctx context.Context, id uuid.UUID) error
	// Reschedule keeps a message pending and moves its next attempt.
	Reschedule(ctx context.Context, id uuid.UUID, availableAt time.Time, lastErr string) error
	// MarkFailed parks a message that must not be retried.
	MarkFailed(ctx context.Context, id uuid.UUID, lastErr string) error
	// PendingCount reports how many messages still wait for delivery.
	PendingCount(ctx context.Context) (int, error)
}

// MerchantIdentity is what the merchant directory tells us about a caller.
type MerchantIdentity struct {
	MerchantID uuid.UUID
	Active     bool
}

// MerchantProfile is the commercial profile an order is placed against.
type MerchantProfile struct {
	ID                uuid.UUID
	Name              string
	Active            bool
	IsAcceptingOrders bool
	MinOrderMinor     int64
	DeliveryFeeMinor  int64
	AvgPrepMinutes    int32
}

// MerchantDirectory is the port to merchant-service. order-service owns
// neither merchant identity nor the commercial profile and asks their owner.
type MerchantDirectory interface {
	// Authenticate resolves a merchant access key, or returns
	// ErrMerchantUnauthenticated.
	Authenticate(ctx context.Context, apiKey string) (MerchantIdentity, error)
	// GetMerchant returns the commercial profile, or ErrMerchantNotFound.
	GetMerchant(ctx context.Context, merchantID uuid.UUID) (MerchantProfile, error)
}

// QuotedItem is one position as the catalogue currently sells it.
type QuotedItem struct {
	MenuItemID     uuid.UUID
	ExternalID     string
	Name           string
	UnitPriceMinor int64
	Problem        domain.ItemProblem
}

// Quote is the catalogue answer for a batch of positions.
type Quote struct {
	MenuVersion int64
	Items       []QuotedItem
}

// MenuCatalogue is the port to catalog-service.
type MenuCatalogue interface {
	// QuoteItems resolves positions to their current names and prices. A
	// position that cannot be sold carries a problem instead of failing the
	// call.
	QuoteItems(ctx context.Context, merchantID uuid.UUID, menuItemIDs []uuid.UUID) (Quote, error)
}

// Availability is the per-unit state of one position.
type Availability struct {
	MenuItemID   uuid.UUID
	Tracked      bool
	SalesStopped bool
	AvailableQty int32
}

// ReserveResult is the outcome of a successful hold.
type ReserveResult struct {
	ExpiresAt time.Time
	// AlreadyExisted reports that the hold with this identifier was already
	// taken, so nothing was held twice.
	AlreadyExisted bool
}

// RejectedLine is one position inventory-service refused to hold.
type RejectedLine struct {
	MenuItemID   uuid.UUID
	RequestedQty int32
	AvailableQty int32
	Problem      domain.ItemProblem
}

// StockLedger is the port to inventory-service.
type StockLedger interface {
	// GetAvailability reports how many units of each position can still be
	// ordered. Read-only.
	GetAvailability(ctx context.Context, merchantID uuid.UUID,
		menuItemIDs []uuid.UUID) ([]Availability, error)
	// Reserve holds the units of every line atomically and is idempotent by
	// reservationID. A refusal returns *ReserveRejectedError describing every
	// requested line.
	Reserve(ctx context.Context, reservationID, merchantID uuid.UUID,
		lines []domain.Line, ttl time.Duration) (ReserveResult, error)
	// Commit consumes the held units. Idempotent.
	Commit(ctx context.Context, reservationID uuid.UUID) error
	// Release returns the held units to availability. Idempotent.
	Release(ctx context.Context, reservationID uuid.UUID, reason domain.ReleaseReason) error
}
