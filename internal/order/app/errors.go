package app

import (
	"errors"
	"fmt"
	"strings"
)

// Errors raised by the use cases around their dependencies. They are kept in
// the application layer because they describe an integration outcome, not a
// rule of the order domain.
var (
	// ErrMerchantUnauthenticated means the presented access key is unknown,
	// revoked or belongs to a merchant disconnected by the operator.
	ErrMerchantUnauthenticated = errors.New("merchant is not authenticated")
	// ErrMerchantNotFound means the merchant directory knows no such merchant.
	ErrMerchantNotFound = errors.New("merchant not found")
	// ErrDependencyUnavailable means a service this one depends on could not
	// be reached or did not answer in time. It is deliberately distinct from
	// every business refusal: an outage must never be reported to the client
	// as "your order is invalid".
	ErrDependencyUnavailable = errors.New("dependency is unavailable")
	// ErrReservationNotFound means inventory-service holds no reservation for
	// the order. For the dispatcher this is permanent, not transient.
	ErrReservationNotFound = errors.New("reservation not found")
	// ErrReservationNotHeld means the hold already reached a terminal state,
	// so the decision cannot be applied again. The effect is already there.
	ErrReservationNotHeld = errors.New("reservation is not held")
	// ErrInvalidRequest means a dependency refused our message as malformed.
	// Retrying an identical malformed message cannot succeed.
	ErrInvalidRequest = errors.New("dependency rejected the request as invalid")
)

// DependencyError names the dependency that failed, so the log says which one
// while the client only ever sees 503.
type DependencyError struct {
	Dependency string
	Cause      error
}

func (e *DependencyError) Error() string {
	return fmt.Sprintf("%s is unavailable: %v", e.Dependency, e.Cause)
}

// Unwrap ties the typed failure to the sentinel and keeps the cause reachable.
func (e *DependencyError) Unwrap() []error {
	return []error{ErrDependencyUnavailable, e.Cause}
}

// Unavailable wraps a transport failure of one dependency.
func Unavailable(dependency string, cause error) error {
	return &DependencyError{Dependency: dependency, Cause: cause}
}

// ReserveRejectedError reports that units could not be held. It carries the
// state of every requested line, not only the first failing one.
type ReserveRejectedError struct {
	Lines []RejectedLine
}

func (e *ReserveRejectedError) Error() string {
	parts := make([]string, 0, len(e.Lines))
	for _, l := range e.Lines {
		parts = append(parts, fmt.Sprintf("%s: %s (requested %d, available %d)",
			l.MenuItemID, l.Problem, l.RequestedQty, l.AvailableQty))
	}
	return "reservation rejected: " + strings.Join(parts, "; ")
}

// AsReserveRejected extracts a *ReserveRejectedError from err, if present.
func AsReserveRejected(err error) (*ReserveRejectedError, bool) {
	var v *ReserveRejectedError
	ok := errors.As(err, &v)
	return v, ok
}
