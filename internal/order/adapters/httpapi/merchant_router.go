package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/google/uuid"
)

type ctxKey int

const merchantCtxKey ctxKey = iota

func (h *Handler) registerMerchantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/merchant/orders", h.handle(h.withAPIKey(h.listMerchantOrders)))
	mux.HandleFunc("GET /api/v1/merchant/orders/{order_id}", h.handle(h.withAPIKey(h.getMerchantOrder)))
	mux.HandleFunc("POST /api/v1/merchant/orders/{order_id}/accept", h.handle(h.withAPIKey(h.acceptOrder)))
	mux.HandleFunc("POST /api/v1/merchant/orders/{order_id}/reject", h.handle(h.withAPIKey(h.rejectOrder)))
	mux.HandleFunc("POST /api/v1/merchant/orders/{order_id}/status", h.handle(h.withAPIKey(h.advanceOrder)))
}

// withAPIKey authenticates the merchant through merchant-service and stores
// the resolved identity in the request context.
func (h *Handler) withAPIKey(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		raw := r.Header.Get(HeaderAPIKey)
		if raw == "" {
			return httpx.Unauthorized("missing " + HeaderAPIKey + " header")
		}
		identity, err := h.svc.Authenticate(r.Context(), raw)
		if err != nil {
			return toHTTPError(err)
		}
		ctx := context.WithValue(r.Context(), merchantCtxKey, identity)
		return next(w, r.WithContext(ctx))
	}
}

func merchantFromContext(ctx context.Context) (app.MerchantIdentity, bool) {
	id, ok := ctx.Value(merchantCtxKey).(app.MerchantIdentity)
	return id, ok
}

// merchantContext resolves the authenticated merchant and the order in the
// path in one place, because every merchant operation needs both.
func (h *Handler) merchantContext(r *http.Request) (uuid.UUID, uuid.UUID, error) {
	identity, ok := merchantFromContext(r.Context())
	if !ok {
		return uuid.Nil, uuid.Nil, httpx.Internal(nil)
	}
	orderID, err := pathOrderID(r)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return identity.MerchantID, orderID, nil
}

// listMerchantOrders serves the queue the outlet polls. Without a status
// filter every status is listed, as api/openapi/order.v1.yaml specifies; a
// till that only wants the work still needing a decision or a move asks for
// those statuses explicitly.
func (h *Handler) listMerchantOrders(w http.ResponseWriter, r *http.Request) error {
	identity, ok := merchantFromContext(r.Context())
	if !ok {
		return httpx.Internal(nil)
	}
	page, err := httpx.ParsePage(r, defaultPageLimit, maxPageLimit)
	if err != nil {
		return err
	}
	statuses, err := parseStatusFilter(r.URL.Query()["status"])
	if err != nil {
		return err
	}
	result, err := h.svc.ListMerchantOrders(r.Context(), identity.MerchantID,
		statuses, page.Limit, page.Offset)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toOrderPageResponse(result))
	return nil
}

// parseStatusFilter accepts the statuses either as repeated query parameters
// or as one comma-separated value, because both are common in web clients.
func parseStatusFilter(raw []string) ([]domain.Status, error) {
	var out []domain.Status
	for _, value := range raw {
		for _, part := range strings.Split(value, ",") {
			part = strings.ToUpper(strings.TrimSpace(part))
			if part == "" {
				continue
			}
			st := domain.Status(part)
			if !st.Valid() {
				return nil, httpx.Unprocessable("status contains an unknown value").
					WithDetails(httpx.FieldViolation{Field: "status", Reason: "unknown status " + part})
			}
			out = append(out, st)
		}
	}
	return out, nil
}

func (h *Handler) getMerchantOrder(w http.ResponseWriter, r *http.Request) error {
	merchantID, orderID, err := h.merchantContext(r)
	if err != nil {
		return err
	}
	order, err := h.svc.GetMerchantOrder(r.Context(), merchantID, orderID)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toOrderResponse(order))
	return nil
}

// acceptOrder confirms an order. Repeating it returns 200 with the unchanged
// order, which is what makes the operation safe to retry without a header.
func (h *Handler) acceptOrder(w http.ResponseWriter, r *http.Request) error {
	merchantID, orderID, err := h.merchantContext(r)
	if err != nil {
		return err
	}
	var body acceptRequest
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			return err
		}
	}
	result, err := h.svc.AcceptOrder(r.Context(), merchantID, orderID, body.ETAMinutes)
	if err != nil {
		return toHTTPError(err)
	}
	writeTransition(w, r, result)
	return nil
}

func (h *Handler) rejectOrder(w http.ResponseWriter, r *http.Request) error {
	merchantID, orderID, err := h.merchantContext(r)
	if err != nil {
		return err
	}
	var body rejectRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	result, err := h.svc.RejectOrder(r.Context(), merchantID, orderID, body.Reason)
	if err != nil {
		return toHTTPError(err)
	}
	writeTransition(w, r, result)
	return nil
}

func (h *Handler) advanceOrder(w http.ResponseWriter, r *http.Request) error {
	merchantID, orderID, err := h.merchantContext(r)
	if err != nil {
		return err
	}
	var body statusRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	target := domain.Status(strings.ToUpper(strings.TrimSpace(body.Status)))
	result, err := h.svc.AdvanceOrder(r.Context(), merchantID, orderID, target)
	if err != nil {
		return toHTTPError(err)
	}
	writeTransition(w, r, result)
	return nil
}

// writeTransition answers a lifecycle move. A repeat of a move that already
// happened is reported with a header rather than a different status code: the
// caller asked for a state, and the order is in that state.
func writeTransition(w http.ResponseWriter, r *http.Request, result app.TransitionResult) {
	if result.AlreadyApplied {
		w.Header().Set("Idempotent-Replay", "true")
	}
	httpx.WriteJSON(w, r, http.StatusOK, toOrderResponse(result.Order))
}
