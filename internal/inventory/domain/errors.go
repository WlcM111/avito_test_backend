// Package domain holds the business entities and rules of inventory-service:
// the per-unit accounting of menu items. It depends on the standard library
// and on uuid only; it knows nothing about HTTP, gRPC or SQL.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors of the inventory domain.
var (
	// ErrStockItemNotFound means no stock card exists for the requested pair
	// (merchant, menu item). The menu item may still exist in the catalogue:
	// inventory-service only knows the cards a merchant has created.
	ErrStockItemNotFound = errors.New("stock item not found")
	// ErrReservationNotFound means no reservation exists with the given id.
	ErrReservationNotFound = errors.New("reservation not found")
	// ErrReservationNotHeld means the reservation already reached a terminal
	// state, so the requested transition is no longer possible.
	ErrReservationNotHeld = errors.New("reservation is not held")
	// ErrVersionConflict means the caller's expected card version is stale.
	ErrVersionConflict = errors.New("stock item version conflict")
	// ErrBelowReserved means the requested on-hand quantity is lower than the
	// number of units currently held for orders. Silently dropping held units
	// is not allowed, so the operation is rejected instead of clamped.
	ErrBelowReserved = errors.New("on hand quantity is below the reserved quantity")
	// ErrNegativeResult means an adjustment would drive the on-hand quantity
	// below zero.
	ErrNegativeResult = errors.New("adjustment would make the on hand quantity negative")
)

// Violation is a single failed field rule.
type Violation struct {
	Field  string
	Reason string
}

// ValidationError aggregates all failed field rules of one input.
type ValidationError struct {
	Violations []Violation
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, v := range e.Violations {
		parts = append(parts, fmt.Sprintf("%s: %s", v.Field, v.Reason))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Add appends a violation.
func (e *ValidationError) Add(field, reason string) {
	e.Violations = append(e.Violations, Violation{Field: field, Reason: reason})
}

// OrNil returns the error when it carries violations, otherwise nil.
func (e *ValidationError) OrNil() error {
	if len(e.Violations) == 0 {
		return nil
	}
	return e
}

// AsValidationError extracts a *ValidationError from err, if present.
func AsValidationError(err error) (*ValidationError, bool) {
	var v *ValidationError
	ok := errors.As(err, &v)
	return v, ok
}

// ReservationRejectedError reports that a reservation could not be taken. It
// carries the state of every requested line, not only the first failing one,
// so that the caller can show the customer everything that is wrong at once.
type ReservationRejectedError struct {
	Lines []LineResult
}

func (e *ReservationRejectedError) Error() string {
	parts := make([]string, 0, len(e.Lines))
	for _, l := range e.Lines {
		if l.Problem == LineProblemNone {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s (requested %d, available %d)",
			l.MenuItemID, l.Problem, l.RequestedQty, l.AvailableQty))
	}
	return "reservation rejected: " + strings.Join(parts, "; ")
}

// AsReservationRejected extracts a *ReservationRejectedError from err.
func AsReservationRejected(err error) (*ReservationRejectedError, bool) {
	var v *ReservationRejectedError
	ok := errors.As(err, &v)
	return v, ok
}
