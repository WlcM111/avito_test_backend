package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/google/uuid"
)

// HeaderAPIKey carries the merchant access key on merchant-facing endpoints.
const HeaderAPIKey = "X-Api-Key"

const (
	defaultPageLimit = 20
	maxPageLimit     = 50
)

type ctxKey int

const merchantCtxKey ctxKey = iota

// Handler serves the external HTTP contract of inventory-service.
type Handler struct {
	svc    *app.Service
	logger *slog.Logger
}

// NewHandler wires the HTTP adapter to the use cases.
func NewHandler(svc *app.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Routes returns the router of the public API. Paths and methods are fixed by
// api/openapi/inventory.v1.yaml.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	// Customer-facing endpoint: no authentication by design.
	mux.HandleFunc("GET /api/v1/merchants/{merchant_id}/availability", h.handle(h.getAvailability))
	// Merchant-facing endpoints: authenticated by access key.
	mux.HandleFunc("GET /api/v1/merchant/stock", h.handle(h.withAPIKey(h.listStock)))
	mux.HandleFunc("GET /api/v1/merchant/stock/{menu_item_id}", h.handle(h.withAPIKey(h.getStockItem)))
	mux.HandleFunc("PUT /api/v1/merchant/stock/{menu_item_id}", h.handle(h.withAPIKey(h.setOnHand)))
	mux.HandleFunc("PATCH /api/v1/merchant/stock/{menu_item_id}", h.handle(h.withAPIKey(h.setSalesState)))
	mux.HandleFunc("POST /api/v1/merchant/stock/{menu_item_id}/adjust", h.handle(h.withAPIKey(h.adjustOnHand)))
	mux.HandleFunc("GET /api/v1/merchant/stock/{menu_item_id}/movements", h.handle(h.withAPIKey(h.listMovements)))
	mux.HandleFunc("/", h.handle(h.notFound))
	return mux
}

// handlerFunc reports failures as errors so that the mapping to the wire
// format happens in exactly one place.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (h *Handler) handle(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			httpx.WriteError(w, r, h.logger, err)
		}
	}
}

func (h *Handler) notFound(_ http.ResponseWriter, _ *http.Request) error {
	return httpx.NotFound("unknown endpoint")
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

func (h *Handler) getAvailability(w http.ResponseWriter, r *http.Request) error {
	merchantID, err := uuid.Parse(r.PathValue("merchant_id"))
	if err != nil {
		return httpx.Unprocessable("merchant_id must be a UUID").
			WithDetails(httpx.FieldViolation{Field: "merchant_id", Reason: "not a UUID"})
	}
	ids, err := parseMenuItemIDs(r.URL.Query()["menu_item_ids"])
	if err != nil {
		return err
	}
	items, err := h.svc.GetAvailability(r.Context(), merchantID, ids)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toAvailabilityResponse(merchantID.String(), items))
	return nil
}

func (h *Handler) listStock(w http.ResponseWriter, r *http.Request) error {
	identity, ok := merchantFromContext(r.Context())
	if !ok {
		return httpx.Internal(nil)
	}
	page, err := httpx.ParsePage(r, defaultPageLimit, maxPageLimit)
	if err != nil {
		return err
	}
	result, err := h.svc.ListStock(r.Context(), identity.MerchantID, page.Limit, page.Offset)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toStockPageResponse(result))
	return nil
}

func (h *Handler) getStockItem(w http.ResponseWriter, r *http.Request) error {
	identity, menuItemID, err := h.itemContext(r)
	if err != nil {
		return err
	}
	item, err := h.svc.GetStockItem(r.Context(), identity.MerchantID, menuItemID)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(item.Version))
	httpx.WriteJSON(w, r, http.StatusOK, toStockItemResponse(item))
	return nil
}

func (h *Handler) setOnHand(w http.ResponseWriter, r *http.Request) error {
	identity, menuItemID, err := h.itemContext(r)
	if err != nil {
		return err
	}
	var body setOnHandRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	if body.OnHandQty == nil {
		return httpx.Unprocessable("on_hand_qty is required").
			WithDetails(httpx.FieldViolation{Field: "on_hand_qty", Reason: "is required"})
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		return err
	}
	item, err := h.svc.SetOnHand(r.Context(), identity.MerchantID, menuItemID,
		*body.OnHandQty, expected, body.Reason)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(item.Version))
	httpx.WriteJSON(w, r, http.StatusOK, toStockItemResponse(item))
	return nil
}

func (h *Handler) adjustOnHand(w http.ResponseWriter, r *http.Request) error {
	identity, menuItemID, err := h.itemContext(r)
	if err != nil {
		return err
	}
	var body adjustRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	if body.Delta == nil {
		return httpx.Unprocessable("delta is required").
			WithDetails(httpx.FieldViolation{Field: "delta", Reason: "is required"})
	}
	result, err := h.svc.AdjustOnHand(r.Context(), identity.MerchantID, menuItemID,
		*body.Delta, strings.TrimSpace(body.OperationKey), body.Reason)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(result.Item.Version))
	httpx.WriteJSON(w, r, http.StatusOK, adjustResponse{
		stockItemResponse: toStockItemResponse(result.Item),
		Duplicate:         result.Duplicate,
	})
	return nil
}

func (h *Handler) setSalesState(w http.ResponseWriter, r *http.Request) error {
	identity, menuItemID, err := h.itemContext(r)
	if err != nil {
		return err
	}
	var body salesStateRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		return err
	}
	item, err := h.svc.SetSalesState(r.Context(), identity.MerchantID, menuItemID,
		domain.SalesState(strings.ToUpper(strings.TrimSpace(body.SalesState))), expected)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(item.Version))
	httpx.WriteJSON(w, r, http.StatusOK, toStockItemResponse(item))
	return nil
}

func (h *Handler) listMovements(w http.ResponseWriter, r *http.Request) error {
	identity, menuItemID, err := h.itemContext(r)
	if err != nil {
		return err
	}
	page, err := httpx.ParsePage(r, defaultPageLimit, maxPageLimit)
	if err != nil {
		return err
	}
	result, err := h.svc.ListMovements(r.Context(), identity.MerchantID, menuItemID,
		page.Limit, page.Offset)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toMovementPageResponse(result))
	return nil
}

func (h *Handler) itemContext(r *http.Request) (app.MerchantIdentity, uuid.UUID, error) {
	identity, ok := merchantFromContext(r.Context())
	if !ok {
		return app.MerchantIdentity{}, uuid.Nil, httpx.Internal(nil)
	}
	menuItemID, err := uuid.Parse(r.PathValue("menu_item_id"))
	if err != nil {
		return app.MerchantIdentity{}, uuid.Nil,
			httpx.Unprocessable("menu_item_id must be a UUID").
				WithDetails(httpx.FieldViolation{Field: "menu_item_id", Reason: "not a UUID"})
	}
	return identity, menuItemID, nil
}

// parseMenuItemIDs accepts the identifiers either as repeated query parameters
// or as one comma-separated value, because both are common in web clients.
func parseMenuItemIDs(raw []string) ([]uuid.UUID, error) {
	var parts []string
	for _, v := range raw {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
	}
	if len(parts) == 0 {
		return nil, httpx.Unprocessable("menu_item_ids is required").
			WithDetails(httpx.FieldViolation{Field: "menu_item_ids", Reason: "is required"})
	}
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		id, err := uuid.Parse(p)
		if err != nil {
			return nil, httpx.Unprocessable("menu_item_ids must contain UUIDs").
				WithDetails(httpx.FieldViolation{Field: "menu_item_ids", Reason: "contains a non-UUID value"})
		}
		out = append(out, id)
	}
	return out, nil
}

func etagFor(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// parseIfMatch reads the optional optimistic concurrency precondition. An
// absent header means "apply unconditionally"; a malformed one is rejected
// rather than ignored, because ignoring it would break the guarantee the
// client asked for.
func parseIfMatch(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return nil, nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil, httpx.Unprocessable("If-Match must contain the stock card version").
			WithDetails(httpx.FieldViolation{Field: "If-Match", Reason: "not a positive integer"})
	}
	return &v, nil
}
