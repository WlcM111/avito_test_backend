package domain

import (
	"time"

	"github.com/google/uuid"
)

// ReservationState is the lifecycle of one hold of units.
type ReservationState string

const (
	// ReservationHeld means units are withdrawn from availability but not yet
	// consumed. This is the only non-terminal state.
	ReservationHeld ReservationState = "HELD"
	// ReservationCommitted means the held units were consumed by the order.
	ReservationCommitted ReservationState = "COMMITTED"
	// ReservationReleased means the held units returned to availability.
	ReservationReleased ReservationState = "RELEASED"
)

// Valid reports whether s is a known reservation state.
func (s ReservationState) Valid() bool {
	return s == ReservationHeld || s == ReservationCommitted || s == ReservationReleased
}

// Terminal reports whether no further transition is possible.
func (s ReservationState) Terminal() bool {
	return s == ReservationCommitted || s == ReservationReleased
}

// CanTransitionTo reports whether the transition is allowed. Only HELD may
// move, and only into a terminal state, which is what makes the transition
// happen exactly once (INV-S5).
func (s ReservationState) CanTransitionTo(next ReservationState) bool {
	return s == ReservationHeld && next.Terminal()
}

// ReleaseReason explains why held units returned to availability.
type ReleaseReason string

const (
	// ReleaseOrderRejected - the merchant rejected the order.
	ReleaseOrderRejected ReleaseReason = "ORDER_REJECTED"
	// ReleaseOrderCancelled - the customer cancelled the order.
	ReleaseOrderCancelled ReleaseReason = "ORDER_CANCELLED"
	// ReleaseOrderExpired - the order expired before the merchant answered.
	ReleaseOrderExpired ReleaseReason = "ORDER_EXPIRED"
	// ReleaseTTLExpired - the hold itself outlived its TTL and was swept.
	// It is produced by the sweeper, never by a caller.
	ReleaseTTLExpired ReleaseReason = "TTL_EXPIRED"
)

// Valid reports whether r is a reason a caller may supply.
func (r ReleaseReason) Valid() bool {
	return r == ReleaseOrderRejected || r == ReleaseOrderCancelled || r == ReleaseOrderExpired
}

// ReservationLine is one item of a hold: a number of fungible units of one
// stock card.
type ReservationLine struct {
	StockItemID uuid.UUID
	MenuItemID  uuid.UUID
	Qty         int32
}

// Reservation is a hold of units taken for one order. Its identifier equals
// the order identifier, which is what makes Reserve idempotent (INV-S6).
type Reservation struct {
	ID         uuid.UUID
	MerchantID uuid.UUID
	State      ReservationState
	ExpiresAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Lines      []ReservationLine
}

// LineProblem explains why one requested line cannot be reserved.
type LineProblem string

const (
	// LineProblemNone means the line can be reserved.
	LineProblemNone LineProblem = "NONE"
	// LineProblemNotTracked means the merchant has no stock card for the item.
	LineProblemNotTracked LineProblem = "NOT_TRACKED"
	// LineProblemSalesStopped means the item is on the stop list.
	LineProblemSalesStopped LineProblem = "SALES_STOPPED"
	// LineProblemInsufficientUnits means fewer units are available than asked.
	LineProblemInsufficientUnits LineProblem = "INSUFFICIENT_UNITS"
)

// LineResult reports the outcome of one requested line.
type LineResult struct {
	MenuItemID   uuid.UUID
	RequestedQty int32
	AvailableQty int32
	Problem      LineProblem
}

// ReserveRequest is a validated request to hold units.
type ReserveRequest struct {
	ReservationID uuid.UUID
	MerchantID    uuid.UUID
	Lines         []ReserveLine
	TTL           time.Duration
}

// ReserveLine is one requested item and quantity.
type ReserveLine struct {
	MenuItemID uuid.UUID
	Qty        int32
}

// NormalizeLines collapses duplicate menu items by summing their quantities
// and returns the lines sorted by menu item id. The order is deterministic on
// purpose: rows are locked in that order inside the reserving transaction, so
// two concurrent multi-item reservations cannot deadlock against each other.
func NormalizeLines(lines []ReserveLine) []ReserveLine {
	if len(lines) == 0 {
		return nil
	}
	merged := make(map[uuid.UUID]int32, len(lines))
	order := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		if _, seen := merged[l.MenuItemID]; !seen {
			order = append(order, l.MenuItemID)
		}
		merged[l.MenuItemID] += l.Qty
	}
	sortUUIDs(order)
	out := make([]ReserveLine, 0, len(order))
	for _, id := range order {
		out = append(out, ReserveLine{MenuItemID: id, Qty: merged[id]})
	}
	return out
}

// sortUUIDs sorts identifiers by their byte representation. Insertion sort is
// used because a reservation carries at most MaxReservationLines items.
func sortUUIDs(ids []uuid.UUID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && lessUUID(ids[j], ids[j-1]); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func lessUUID(a, b uuid.UUID) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// Validate checks the request against the business bounds. It expects the
// lines to be already normalized.
func (r ReserveRequest) Validate() error {
	ve := &ValidationError{}
	if r.ReservationID == uuid.Nil {
		ve.Add("reservation_id", "is required")
	}
	if r.MerchantID == uuid.Nil {
		ve.Add("merchant_id", "is required")
	}
	switch {
	case len(r.Lines) == 0:
		ve.Add("lines", "must contain at least one item")
	case len(r.Lines) > MaxReservationLines:
		ve.Add("lines", "must not contain more than 50 items")
	}
	var total int32
	for _, l := range r.Lines {
		if l.MenuItemID == uuid.Nil {
			ve.Add("lines.menu_item_id", "is required")
		}
		if l.Qty < 1 || l.Qty > MaxLineQty {
			ve.Add("lines.qty", "must be between 1 and 99")
		}
		total += l.Qty
	}
	if total > MaxReservationUnits {
		ve.Add("lines", "must not request more than 200 units in total")
	}
	if r.TTL <= 0 {
		ve.Add("ttl_seconds", "must be greater than zero")
	}
	return ve.OrNil()
}
