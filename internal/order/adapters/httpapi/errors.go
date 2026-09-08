package httpapi

import (
	"errors"
	"net/http"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
)

// Domain-specific error codes of the external contract.
const (
	codeOrderNotFound      = "ORDER_NOT_FOUND"
	codeOrderStateConflict = "ORDER_STATE_CONFLICT"
	codeNotOrderable       = "ORDER_NOT_ORDERABLE"
	codePriceChanged       = "PRICE_CHANGED"
	codeMerchantNotFound   = "MERCHANT_NOT_FOUND"
)

// toHTTPError maps a domain or application error onto the shared envelope.
// Unmapped errors become 500 without leaking the cause to the client.
func toHTTPError(err error) error {
	switch {
	case errors.Is(err, domain.ErrOrderNotFound):
		return httpx.NewError(http.StatusNotFound, codeOrderNotFound, "order not found")
	case errors.Is(err, app.ErrMerchantNotFound):
		return httpx.NewError(http.StatusNotFound, codeMerchantNotFound, "merchant not found")
	case errors.Is(err, domain.ErrPriceChanged):
		return httpx.Conflict(codePriceChanged,
			"the order total changed since the preview; repeat the preview and confirm the new total")
	case errors.Is(err, app.ErrMerchantUnauthenticated):
		return httpx.Unauthorized("invalid or revoked api key")
	case errors.Is(err, app.ErrDependencyUnavailable):
		return httpx.Unavailable("a dependency of order-service is unavailable", err)
	}
	if conflict, ok := domain.AsStateConflict(err); ok {
		return httpx.Conflict(codeOrderStateConflict,
			"the order is in status "+string(conflict.Current)+
				" and cannot move to "+string(conflict.Target)).
			WithDetails(httpx.FieldViolation{Field: "status", Reason: "current: " + string(conflict.Current)})
	}
	if notOrderable, ok := domain.AsNotOrderable(err); ok {
		return notOrderableError(notOrderable)
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

// notOrderableError turns a refused basket into a 409 whose code names the
// single dominant reason and whose details list every problem. One code is not
// enough for a basket with several problems, and a bare list is not enough for
// a client that branches on the code.
func notOrderableError(err *domain.NotOrderableError) error {
	out := httpx.Conflict(dominantCode(err), "the order cannot be placed")
	for _, problem := range err.Problems {
		out = out.WithDetails(httpx.FieldViolation{Field: "order", Reason: string(problem)})
	}
	for _, line := range err.Lines {
		if line.Problem == domain.ItemProblemNone {
			continue
		}
		out = out.WithDetails(httpx.FieldViolation{
			Field:  "items." + line.MenuItemID.String(),
			Reason: string(line.Problem),
		})
	}
	return out
}

// dominantCode picks the code a client should branch on. Order-level problems
// come first because they make every position irrelevant, and among the
// positions a shortage is reported ahead of a stop list because it is the case
// the customer can fix by ordering fewer units.
func dominantCode(err *domain.NotOrderableError) string {
	for _, problem := range err.Problems {
		return string(problem)
	}
	var stopped, other string
	for _, line := range err.Lines {
		switch line.Problem {
		case domain.ItemProblemNone:
			continue
		case domain.ItemProblemInsufficientUnits:
			return string(domain.ItemProblemInsufficientUnits)
		case domain.ItemProblemSalesStopped:
			stopped = string(domain.ItemProblemSalesStopped)
		default:
			if other == "" {
				other = string(line.Problem)
			}
		}
	}
	switch {
	case stopped != "":
		return stopped
	case other != "":
		return other
	default:
		return codeNotOrderable
	}
}
