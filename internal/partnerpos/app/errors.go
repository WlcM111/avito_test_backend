package app

import (
	"errors"
	"fmt"
)

// ErrPlatformUnavailable means the platform could not be reached or refused
// the call. The till never treats that as a business answer: it keeps its own
// state and retries on the next tick.
var ErrPlatformUnavailable = errors.New("platform is unavailable")

// PlatformError names the call that failed, so a log says which endpoint of
// which service refused.
type PlatformError struct {
	Operation string
	Cause     error
}

func (e *PlatformError) Error() string {
	return fmt.Sprintf("platform call %s failed: %v", e.Operation, e.Cause)
}

// Unwrap ties the typed failure to the sentinel and keeps the cause reachable.
func (e *PlatformError) Unwrap() []error {
	return []error{ErrPlatformUnavailable, e.Cause}
}

// Unavailable wraps a failed platform call.
func Unavailable(operation string, cause error) error {
	return &PlatformError{Operation: operation, Cause: cause}
}
