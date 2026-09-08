package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/google/uuid"
)

// HeaderAPIKey carries the merchant access key on merchant-facing endpoints.
const HeaderAPIKey = "X-Api-Key"

type ctxKey int

const merchantCtxKey ctxKey = iota

// Handler serves the external HTTP contract of catalog-service.
type Handler struct {
	svc    *app.Service
	logger *slog.Logger
}

// NewHandler wires the HTTP adapter to the use cases.
func NewHandler(svc *app.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Routes returns the router of the public API. Paths and methods are fixed by
// api/openapi/catalog.v1.yaml.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	// Customer-facing endpoint: no authentication by design.
	mux.HandleFunc("GET /api/v1/merchants/{merchant_id}/menu", h.handle(h.getPublicMenu))
	// Merchant-facing endpoints: authenticated by access key.
	mux.HandleFunc("GET /api/v1/merchant/menu", h.handle(h.withAPIKey(h.getOwnMenu)))
	mux.HandleFunc("PUT /api/v1/merchant/menu", h.handle(h.withAPIKey(h.publishMenu)))
	mux.HandleFunc("PATCH /api/v1/merchant/menu/items/{item_id}",
		h.handle(h.withAPIKey(h.patchMenuItem)))
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

func (h *Handler) getPublicMenu(w http.ResponseWriter, r *http.Request) error {
	merchantID, err := uuid.Parse(r.PathValue("merchant_id"))
	if err != nil {
		return httpx.Unprocessable("merchant_id must be a UUID").
			WithDetails(httpx.FieldViolation{Field: "merchant_id", Reason: "not a UUID"})
	}
	menu, err := h.svc.GetPublicMenu(r.Context(), merchantID)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(menu.MenuVersion))
	httpx.WriteJSON(w, r, http.StatusOK, toMenuResponse(menu))
	return nil
}

func (h *Handler) getOwnMenu(w http.ResponseWriter, r *http.Request) error {
	identity, ok := merchantFromContext(r.Context())
	if !ok {
		return httpx.Internal(nil)
	}
	menu, err := h.svc.GetOwnMenu(r.Context(), identity.MerchantID)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(menu.MenuVersion))
	httpx.WriteJSON(w, r, http.StatusOK, toMenuResponse(menu))
	return nil
}

func (h *Handler) publishMenu(w http.ResponseWriter, r *http.Request) error {
	identity, ok := merchantFromContext(r.Context())
	if !ok {
		return httpx.Internal(nil)
	}
	var body publishMenuRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		return err
	}
	result, err := h.svc.PublishSnapshot(r.Context(), identity.MerchantID,
		body.toSnapshot(), expected)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(result.Menu.MenuVersion))
	httpx.WriteJSON(w, r, http.StatusOK, toPublishResponse(result))
	return nil
}

func (h *Handler) patchMenuItem(w http.ResponseWriter, r *http.Request) error {
	identity, ok := merchantFromContext(r.Context())
	if !ok {
		return httpx.Internal(nil)
	}
	itemID, err := uuid.Parse(r.PathValue("item_id"))
	if err != nil {
		return httpx.Unprocessable("item_id must be a UUID").
			WithDetails(httpx.FieldViolation{Field: "item_id", Reason: "not a UUID"})
	}
	var body patchItemRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		return err
	}
	result, err := h.svc.UpdateMenuItem(r.Context(), identity.MerchantID, itemID,
		body.toPatch(), expected)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(result.MenuVersion))
	httpx.WriteJSON(w, r, http.StatusOK, toPatchItemResponse(result))
	return nil
}

func etagFor(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// parseIfMatch reads the optional optimistic concurrency precondition, which
// for the catalogue is the menu version. An absent header means "apply
// unconditionally"; a malformed one is rejected rather than ignored, because
// ignoring it would break the guarantee the client asked for.
//
// Zero is accepted: it is the version of a merchant that has never published,
// so "publish only if my menu is still empty" is expressible.
func parseIfMatch(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return nil, nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return nil, httpx.Unprocessable("If-Match must contain the menu version").
			WithDetails(httpx.FieldViolation{Field: "If-Match", Reason: "not a non-negative integer"})
	}
	return &v, nil
}
