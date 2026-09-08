package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors of the till.
var (
	// ErrItemNotFound means the till menu has no position with that external
	// identifier.
	ErrItemNotFound = errors.New("menu item not found")
	// ErrNotPublished means the menu has not been published yet, so the till
	// does not know the platform identifiers of its own positions.
	ErrNotPublished = errors.New("menu has not been published yet")
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
