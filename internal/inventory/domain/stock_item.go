package domain

import (
	"time"

	"github.com/google/uuid"
)

// SalesState is the merchant's stop-list switch for one menu item. It is an
// independent dimension: it never carries information about quantities.
type SalesState string

const (
	// SalesEnabled means the merchant allows the item to be sold.
	SalesEnabled SalesState = "ENABLED"
	// SalesStopped means the item is withdrawn from sale regardless of how
	// many units are on hand.
	SalesStopped SalesState = "STOPPED"
)

// Valid reports whether s is a known sales state.
func (s SalesState) Valid() bool { return s == SalesEnabled || s == SalesStopped }

// Bounds of the accounting quantities. They are business limits and are
// enforced in the domain as well as by CHECK constraints in the schema.
const (
	// MaxOnHandQty caps a single stock card. It exists so that a typo in a
	// merchant integration cannot create an effectively unlimited stock.
	MaxOnHandQty = 1_000_000
	// MaxLineQty caps the units of one line of one reservation.
	MaxLineQty = 99
	// MaxReservationLines caps the number of distinct items in one reservation.
	MaxReservationLines = 50
	// MaxReservationUnits caps the total units of one reservation.
	MaxReservationUnits = 200
	// MaxOperationKeyLength caps the deduplication key of AdjustOnHand.
	MaxOperationKeyLength = 128
	// MaxReasonLength caps the free-text reason recorded in the journal.
	MaxReasonLength = 200
	// MaxAvailabilityBatch caps one availability query.
	MaxAvailabilityBatch = 100
)

// StockItem is the accounting card of one menu item at one merchant. It is the
// object that has identity and a version; individual units do not.
type StockItem struct {
	ID         uuid.UUID
	MerchantID uuid.UUID
	MenuItemID uuid.UUID

	SalesState SalesState

	// OnHandQty is how many units are physically available for sale.
	// Only the merchant changes it.
	OnHandQty int32
	// ReservedQty is how many units are held for unconfirmed orders.
	// Only the platform changes it.
	ReservedQty int32

	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AvailableQty is the number of units that may still be reserved. This is the
// value compared against a requested quantity, never OnHandQty alone.
func (s StockItem) AvailableQty() int32 { return s.OnHandQty - s.ReservedQty }

// Sellable reports whether at least one unit can be ordered right now. Both
// dimensions must allow it: an ENABLED card with zero units is not sellable,
// which is exactly what "enabling sales does not confirm any quantity" means.
func (s StockItem) Sellable() bool {
	return s.SalesState == SalesEnabled && s.AvailableQty() > 0
}

// CanReserve reports whether qty units can be held, and why not otherwise.
func (s StockItem) CanReserve(qty int32) LineProblem {
	if s.SalesState != SalesEnabled {
		return LineProblemSalesStopped
	}
	if s.AvailableQty() < qty {
		return LineProblemInsufficientUnits
	}
	return LineProblemNone
}

// ValidateOnHand checks an absolute on-hand value against the business bounds.
func ValidateOnHand(qty int32) error {
	ve := &ValidationError{}
	if qty < 0 {
		ve.Add("on_hand_qty", "must not be negative")
	} else if qty > MaxOnHandQty {
		ve.Add("on_hand_qty", "must not exceed 1000000")
	}
	return ve.OrNil()
}

// ValidateAdjustment checks a relative change and its deduplication key.
func ValidateAdjustment(delta int32, operationKey, reason string) error {
	ve := &ValidationError{}
	if delta == 0 {
		ve.Add("delta", "must not be zero")
	}
	if delta > MaxOnHandQty || delta < -MaxOnHandQty {
		ve.Add("delta", "must not exceed 1000000 in absolute value")
	}
	if l := len(operationKey); l == 0 {
		ve.Add("operation_key", "is required")
	} else if l > MaxOperationKeyLength {
		ve.Add("operation_key", "must not exceed 128 characters")
	}
	if len([]rune(reason)) > MaxReasonLength {
		ve.Add("reason", "must not exceed 200 characters")
	}
	return ve.OrNil()
}

// ApplyAdjustment computes the new on-hand value of a card and reports the
// domain reason when the change is not allowed. It never clamps: losing held
// units silently would break the accounting.
func (s StockItem) ApplyAdjustment(delta int32) (int32, error) {
	next := s.OnHandQty + delta
	switch {
	case next < 0:
		return 0, ErrNegativeResult
	case next > MaxOnHandQty:
		return 0, &ValidationError{Violations: []Violation{
			{Field: "delta", Reason: "resulting on_hand_qty would exceed 1000000"},
		}}
	case next < s.ReservedQty:
		return 0, ErrBelowReserved
	}
	return next, nil
}
