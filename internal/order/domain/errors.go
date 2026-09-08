// Package domain holds the business entities and rules of order-service: the
// order, its lifecycle and the outbox message that carries a terminal decision
// to inventory-service. It depends on the standard library and on uuid only;
// it knows nothing about HTTP, gRPC or SQL.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors of the order domain.
var (
	// ErrOrderNotFound means no order with the requested identifier belongs to
	// the caller. It is deliberately returned for an order of another customer
	// or another merchant as well: telling an anonymous caller that an order
	// exists but is not theirs would disclose it.
	ErrOrderNotFound = errors.New("order not found")
	// ErrStateConflict means the requested transition is not allowed from the
	// current status.
	ErrStateConflict = errors.New("order state conflict")
	// ErrPriceChanged means the total recalculated at placement time differs
	// from the total the customer confirmed in the preview.
	ErrPriceChanged = errors.New("order total changed since the preview")
)

// Violation is a single failed field rule.
type Violation struct {
	Field  string
	Reason string
}

// ValidationError aggregates all failed field rules of one input. Every rule
// is evaluated, so a client sees every problem of a rejected request at once.
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

// StateConflictError reports a refused transition together with the status the
// order is actually in, so the client can decide what to do next without a
// second request.
type StateConflictError struct {
	Current Status
	Target  Status
}

func (e *StateConflictError) Error() string {
	return fmt.Sprintf("order state conflict: cannot move from %s to %s", e.Current, e.Target)
}

// Unwrap ties the typed conflict to the sentinel, so callers can match either.
func (e *StateConflictError) Unwrap() error { return ErrStateConflict }

// AsStateConflict extracts a *StateConflictError from err, if present.
func AsStateConflict(err error) (*StateConflictError, bool) {
	var v *StateConflictError
	ok := errors.As(err, &v)
	return v, ok
}

// NotOrderableError reports that a draft cannot become an order. It carries
// the outcome of every requested line and every order-level problem, because
// the customer needs to see everything that is wrong at once rather than
// fixing one problem per attempt.
type NotOrderableError struct {
	Lines    []PreviewLine
	Problems []OrderProblem
}

func (e *NotOrderableError) Error() string {
	parts := make([]string, 0, len(e.Lines)+len(e.Problems))
	for _, p := range e.Problems {
		parts = append(parts, string(p))
	}
	for _, l := range e.Lines {
		if l.Problem == ItemProblemNone {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", l.MenuItemID, l.Problem))
	}
	return "order cannot be placed: " + strings.Join(parts, "; ")
}

// AsNotOrderable extracts a *NotOrderableError from err, if present.
func AsNotOrderable(err error) (*NotOrderableError, bool) {
	var v *NotOrderableError
	ok := errors.As(err, &v)
	return v, ok
}
