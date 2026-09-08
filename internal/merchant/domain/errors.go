// Package domain holds the business entities and rules of merchant-service.
// It depends on the standard library and on uuid only; it knows nothing about
// HTTP, gRPC or SQL.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors of the merchant domain.
var (
	// ErrMerchantNotFound means no merchant exists with the requested identity.
	ErrMerchantNotFound = errors.New("merchant not found")
	// ErrAPIKeyNotFound means the presented access key is unknown or revoked.
	ErrAPIKeyNotFound = errors.New("api key not found")
	// ErrVersionConflict means the caller's expected version is stale.
	ErrVersionConflict = errors.New("merchant version conflict")
	// ErrNoChanges means the profile patch carried no fields.
	ErrNoChanges = errors.New("no profile fields to update")
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
