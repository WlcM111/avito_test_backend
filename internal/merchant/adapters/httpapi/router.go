package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/domain"
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

// Handler serves the external HTTP contract of merchant-service.
type Handler struct {
	svc    *app.Service
	logger *slog.Logger
}

// NewHandler wires the HTTP adapter to the use cases.
func NewHandler(svc *app.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Routes returns the router of the public API. Paths and methods are fixed by
// api/openapi/merchant.v1.yaml.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	// Customer-facing endpoints: no authentication by design (the assignment
	// excludes customer authentication from the MVP).
	mux.HandleFunc("GET /api/v1/merchants", h.handle(h.listMerchants))
	mux.HandleFunc("GET /api/v1/merchants/{merchant_id}", h.handle(h.getMerchant))
	// Merchant-facing endpoints: authenticated by access key.
	mux.HandleFunc("GET /api/v1/merchant/profile", h.handle(h.withAPIKey(h.getProfile)))
	mux.HandleFunc("PATCH /api/v1/merchant/profile", h.handle(h.withAPIKey(h.patchProfile)))
	mux.HandleFunc("/", h.handle(h.notFound))
	return mux
}

// handlerFunc is an handler that reports failures as errors so that the
// mapping to the wire format happens in exactly one place.
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

// withAPIKey authenticates the merchant and stores it in the request context.
func (h *Handler) withAPIKey(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		raw := r.Header.Get(HeaderAPIKey)
		if raw == "" {
			return httpx.Unauthorized("missing " + HeaderAPIKey + " header")
		}
		m, err := h.svc.Authenticate(r.Context(), raw)
		if err != nil {
			return toHTTPError(err)
		}
		ctx := context.WithValue(r.Context(), merchantCtxKey, m)
		return next(w, r.WithContext(ctx))
	}
}

func merchantFromContext(ctx context.Context) (domain.Merchant, bool) {
	m, ok := ctx.Value(merchantCtxKey).(domain.Merchant)
	return m, ok
}

func (h *Handler) listMerchants(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, defaultPageLimit, maxPageLimit)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	onlyAccepting := false
	if raw := strings.TrimSpace(q.Get("only_accepting")); raw != "" {
		v, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return httpx.Unprocessable("only_accepting must be a boolean").
				WithDetails(httpx.FieldViolation{Field: "only_accepting", Reason: "not a boolean"})
		}
		onlyAccepting = v
	}

	result, err := h.svc.ListMerchants(r.Context(), domain.ListFilter{
		City:          strings.TrimSpace(q.Get("city")),
		Query:         strings.TrimSpace(q.Get("q")),
		OnlyAccepting: onlyAccepting,
		Limit:         page.Limit,
		Offset:        page.Offset,
	})
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toListResponse(result))
	return nil
}

func (h *Handler) getMerchant(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(r.PathValue("merchant_id"))
	if err != nil {
		return httpx.Unprocessable("merchant_id must be a UUID").
			WithDetails(httpx.FieldViolation{Field: "merchant_id", Reason: "not a UUID"})
	}
	m, err := h.svc.GetMerchant(r.Context(), id)
	if err != nil {
		return toHTTPError(err)
	}
	// Suspended merchants are not part of the public catalogue. Reporting them
	// as absent avoids disclosing the closed list to anonymous callers.
	if m.Status != domain.StatusActive {
		return toHTTPError(domain.ErrMerchantNotFound)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toMerchantResponse(m))
	return nil
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) error {
	m, ok := merchantFromContext(r.Context())
	if !ok {
		return httpx.Internal(nil)
	}
	w.Header().Set("ETag", etagFor(m.Version))
	httpx.WriteJSON(w, r, http.StatusOK, toMerchantResponse(m))
	return nil
}

func (h *Handler) patchProfile(w http.ResponseWriter, r *http.Request) error {
	m, ok := merchantFromContext(r.Context())
	if !ok {
		return httpx.Internal(nil)
	}
	var body patchProfileRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		return err
	}
	updated, err := h.svc.UpdateProfile(r.Context(), m.ID, body.toPatch(), expected)
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("ETag", etagFor(updated.Version))
	httpx.WriteJSON(w, r, http.StatusOK, toMerchantResponse(updated))
	return nil
}

func etagFor(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// parseIfMatch reads the optional optimistic concurrency precondition. An
// absent header means "apply unconditionally"; a malformed one is rejected
// rather than silently ignored, because ignoring it would break the guarantee
// the client asked for.
func parseIfMatch(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if raw == "*" {
		return nil, nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil, httpx.Unprocessable("If-Match must contain the merchant version").
			WithDetails(httpx.FieldViolation{Field: "If-Match", Reason: "not a positive integer"})
	}
	return &v, nil
}
