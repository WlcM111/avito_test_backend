package httpapi

import (
	"errors"
	"net/http"

	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
)

// Domain-specific error codes of the external contract.
const (
	codeStockItemNotFound   = "STOCK_ITEM_NOT_FOUND"
	codeBelowReserved       = "BELOW_RESERVED"
	codeNegativeResult      = "NEGATIVE_ON_HAND"
	codeReservationNotFound = "RESERVATION_NOT_FOUND"
	codeReservationNotHeld  = "RESERVATION_NOT_HELD"
)

// toHTTPError maps a domain or application error onto the shared envelope.
// Unmapped errors become 500 without leaking the cause to the client.
func toHTTPError(err error) error {
	switch {
	case errors.Is(err, domain.ErrStockItemNotFound):
		return httpx.NewError(http.StatusNotFound, codeStockItemNotFound,
			"the merchant keeps no stock card for this menu item")
	case errors.Is(err, domain.ErrReservationNotFound):
		return httpx.NewError(http.StatusNotFound, codeReservationNotFound, "reservation not found")
	case errors.Is(err, domain.ErrReservationNotHeld):
		return httpx.Conflict(codeReservationNotHeld, err.Error())
	case errors.Is(err, domain.ErrBelowReserved):
		return httpx.Conflict(codeBelowReserved,
			"the requested quantity is below the units currently held for orders")
	case errors.Is(err, domain.ErrNegativeResult):
		return httpx.Conflict(codeNegativeResult,
			"the adjustment would make the on hand quantity negative")
	case errors.Is(err, domain.ErrVersionConflict):
		return httpx.PreconditionFailed(
			"the stock card was modified concurrently; re-read it and retry")
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
