package httpapi

import (
	"errors"
	"net/http"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
)

// Domain-specific error codes of the external contract.
const (
	codeMerchantNotFound = "MERCHANT_NOT_FOUND"
	codeNoChanges        = "NO_PROFILE_CHANGES"
)

// toHTTPError maps a domain error onto the shared HTTP envelope. Unmapped
// errors become 500 without leaking the cause to the client.
func toHTTPError(err error) error {
	switch {
	case errors.Is(err, domain.ErrMerchantNotFound):
		return httpx.NewError(http.StatusNotFound, codeMerchantNotFound, "merchant not found")
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		return httpx.Unauthorized("invalid or revoked api key")
	case errors.Is(err, domain.ErrVersionConflict):
		return httpx.PreconditionFailed(
			"merchant profile was modified concurrently; re-read it and retry")
	case errors.Is(err, domain.ErrNoChanges):
		return httpx.NewError(http.StatusUnprocessableEntity, codeNoChanges,
			"request body must contain at least one profile field")
	}
	if ve, ok := domain.AsValidationError(err); ok {
		out := httpx.Unprocessable("request validation failed")
		for _, v := range ve.Violations {
			out = out.WithDetails(httpx.FieldViolation{Field: v.Field, Reason: v.Reason})
		}
		return out
	}
	return httpx.Internal(err)
}
