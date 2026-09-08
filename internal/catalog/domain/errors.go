// Package domain holds the business entities and rules of catalog-service: the
// structure, identity and prices of a merchant menu. It depends on the standard
// library and on uuid only; it knows nothing about HTTP, gRPC or SQL.
//
// The catalogue owns what is sold and for how much. It owns no quantity: how
// many units exist belongs to inventory-service, and publishing a menu neither
// creates nor confirms stock.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors of the catalogue domain.
var (
	// ErrMenuNotFound means the merchant has never published a menu, so the
	// platform holds no catalogue for it.
	ErrMenuNotFound = errors.New("menu not found")
	// ErrMenuItemNotFound means no item with the requested id belongs to the
	// merchant, or the item has been withdrawn from the published menu.
	ErrMenuItemNotFound = errors.New("menu item not found")
	// ErrVersionConflict means the caller's expected menu version is stale:
	// the menu changed between the read and the write.
	ErrVersionConflict = errors.New("menu version conflict")
)

// Violation is a single failed field rule.
type Violation struct {
	Field  string
	Reason string
}

// ValidationError aggregates all failed field rules of one input. Every rule
// is evaluated, so a merchant integration sees every problem of a rejected
// snapshot at once instead of fixing them one publication at a time.
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
