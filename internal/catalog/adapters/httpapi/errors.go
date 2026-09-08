package httpapi

import (
	"errors"
	"net/http"

	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
)

// Domain-specific error codes of the external contract.
const (
	codeMenuNotFound     = "MENU_NOT_FOUND"
	codeMenuItemNotFound = "MENU_ITEM_NOT_FOUND"
)

// toHTTPError maps a domain or application error onto the shared envelope.
// Unmapped errors become 500 without leaking the cause to the client.
func toHTTPError(err error) error {
	switch {
	case errors.Is(err, domain.ErrMenuNotFound):
		return httpx.NewError(http.StatusNotFound, codeMenuNotFound,
			"the merchant has not published a menu")
	case errors.Is(err, domain.ErrMenuItemNotFound):
		return httpx.NewError(http.StatusNotFound, codeMenuItemNotFound,
			"the merchant has no published item with this identifier")
	case errors.Is(err, domain.ErrVersionConflict):
		return httpx.PreconditionFailed(
			"the menu was modified concurrently; re-read it and retry")
	case errors.Is(err, app.ErrMerchantUnauthenticated):
		return httpx.Unauthorized("invalid or revoked api key")
	case errors.Is(err, app.ErrMerchantDirectoryUnavailable):
		return httpx.Unavailable("merchant directory is unavailable", err)
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
