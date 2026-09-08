// Package httpx contains the transport-level helpers shared by the HTTP
// adapters of every service: the error envelope, middleware and the server
// lifecycle. It contains no business rules.
package httpx

import (
	"fmt"
	"net/http"
)

// Machine-readable error codes of the external HTTP contract. The set is
// shared by all services so that clients can handle errors uniformly.
const (
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeNotFound         = "NOT_FOUND"
	CodeConflict         = "CONFLICT"
	CodeVersionConflict  = "VERSION_CONFLICT"
	CodeRateLimited      = "RATE_LIMITED"
	CodePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
	CodeDependencyDown   = "DEPENDENCY_UNAVAILABLE"
	CodeTimeout          = "TIMEOUT"
	CodeInternal         = "INTERNAL_ERROR"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodeUnsupportedMedia = "UNSUPPORTED_MEDIA_TYPE"
)

// FieldViolation describes one invalid input field.
type FieldViolation struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Error is the transport representation of a failure. It carries the HTTP
// status, a stable machine code and optional per-field details. Internal
// causes are kept in Cause and never serialised to the client.
type Error struct {
	Status  int
	Code    string
	Message string
	Details []FieldViolation
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the internal cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// NewError builds a transport error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WithDetails attaches field violations and returns the same error.
func (e *Error) WithDetails(d ...FieldViolation) *Error {
	e.Details = append(e.Details, d...)
	return e
}

// WithCause attaches an internal cause and returns the same error.
func (e *Error) WithCause(err error) *Error {
	e.Cause = err
	return e
}

// Convenience constructors for the most frequent failures.

func BadRequest(message string) *Error {
	return NewError(http.StatusBadRequest, CodeValidationFailed, message)
}

func Unprocessable(message string) *Error {
	return NewError(http.StatusUnprocessableEntity, CodeValidationFailed, message)
}

func Unauthorized(message string) *Error {
	return NewError(http.StatusUnauthorized, CodeUnauthorized, message)
}

func NotFound(message string) *Error {
	return NewError(http.StatusNotFound, CodeNotFound, message)
}

func Conflict(code, message string) *Error {
	return NewError(http.StatusConflict, code, message)
}

func PreconditionFailed(message string) *Error {
	return NewError(http.StatusPreconditionFailed, CodeVersionConflict, message)
}

func Internal(cause error) *Error {
	return NewError(http.StatusInternalServerError, CodeInternal,
		"internal error").WithCause(cause)
}

func Unavailable(message string, cause error) *Error {
	return NewError(http.StatusServiceUnavailable, CodeDependencyDown, message).WithCause(cause)
}
