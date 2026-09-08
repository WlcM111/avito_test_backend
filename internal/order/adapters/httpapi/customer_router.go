package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/google/uuid"
)

// Headers of the external contract (TZ v3.0 §7.1).
const (
	// HeaderCustomerID identifies the customer. It is explicitly NOT a means
	// of authentication: the assignment excludes customer authentication, so
	// the value is taken at face value and only used to scope an order to the
	// client that placed it.
	HeaderCustomerID = "X-Customer-Id"
	// HeaderIdempotencyKey makes a repeated placement return the first order.
	HeaderIdempotencyKey = "Idempotency-Key"
	// HeaderAPIKey carries the merchant access key on merchant endpoints.
	HeaderAPIKey = "X-Api-Key"
)

const (
	defaultPageLimit = 20
	maxPageLimit     = 50
)

// Handler serves the external HTTP contract of order-service.
type Handler struct {
	svc    *app.Service
	logger *slog.Logger
}

// NewHandler wires the HTTP adapter to the use cases.
func NewHandler(svc *app.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Routes returns the router of the public API. Paths and methods are fixed by
// api/openapi/order.v1.yaml. The customer and the merchant side are registered
// separately because they have different audiences and different
// authentication rules, but they share one listener and one error envelope.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	h.registerCustomerRoutes(mux)
	h.registerMerchantRoutes(mux)
	mux.HandleFunc("/", h.handle(h.notFound))
	return mux
}

func (h *Handler) registerCustomerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/orders/preview", h.handle(h.previewOrder))
	mux.HandleFunc("POST /api/v1/orders", h.handle(h.createOrder))
	mux.HandleFunc("GET /api/v1/orders", h.handle(h.listOrders))
	mux.HandleFunc("GET /api/v1/orders/{order_id}", h.handle(h.getOrder))
	mux.HandleFunc("POST /api/v1/orders/{order_id}/cancel", h.handle(h.cancelOrder))
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

// customerID reads the identification header. It is required wherever an order
// is scoped to one customer.
func customerID(r *http.Request) (uuid.UUID, error) {
	raw := strings.TrimSpace(r.Header.Get(HeaderCustomerID))
	if raw == "" {
		return uuid.Nil, httpx.Unprocessable(HeaderCustomerID + " is required").
			WithDetails(httpx.FieldViolation{Field: HeaderCustomerID, Reason: "is required"})
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, httpx.Unprocessable(HeaderCustomerID + " must be a UUID").
			WithDetails(httpx.FieldViolation{Field: HeaderCustomerID, Reason: "not a UUID"})
	}
	return id, nil
}

func pathOrderID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("order_id"))
	if err != nil {
		return uuid.Nil, httpx.Unprocessable("order_id must be a UUID").
			WithDetails(httpx.FieldViolation{Field: "order_id", Reason: "not a UUID"})
	}
	return id, nil
}

// parseMerchantID converts the merchant identifier of a request body.
func parseMerchantID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, httpx.Unprocessable("merchant_id must be a UUID").
			WithDetails(httpx.FieldViolation{Field: "merchant_id", Reason: "not a UUID"})
	}
	return id, nil
}

// badItemIDs turns unparsable position identifiers into one validation error.
func badItemIDs(bad []string) error {
	out := httpx.Unprocessable("items contain a position that is not a UUID")
	for _, raw := range bad {
		out = out.WithDetails(httpx.FieldViolation{
			Field: "items.menu_item_id", Reason: "not a UUID: " + raw,
		})
	}
	return out
}

// previewOrder recalculates a basket. It creates nothing and holds nothing, so
// it needs no idempotency key and no customer identifier.
func (h *Handler) previewOrder(w http.ResponseWriter, r *http.Request) error {
	var body previewRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	merchantID, err := parseMerchantID(body.MerchantID)
	if err != nil {
		return err
	}
	lines, bad := toLines(body.Items)
	if len(bad) > 0 {
		return badItemIDs(bad)
	}

	preview, err := h.svc.PreviewOrder(r.Context(), domain.Draft{
		MerchantID: merchantID,
		Lines:      lines,
	})
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toPreviewResponse(preview))
	return nil
}

// createOrder places an order. A repeat with the same Idempotency-Key returns
// the order created the first time, with the same 201, because the client
// cannot tell the two calls apart and must not be told the order failed.
func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) error {
	customer, err := customerID(r)
	if err != nil {
		return err
	}
	var body createRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	merchantID, err := parseMerchantID(body.MerchantID)
	if err != nil {
		return err
	}
	lines, bad := toLines(body.Items)
	if len(bad) > 0 {
		return badItemIDs(bad)
	}

	result, err := h.svc.CreateOrder(r.Context(), domain.Draft{
		MerchantID:         merchantID,
		CustomerID:         customer,
		Lines:              lines,
		CustomerName:       body.CustomerName,
		CustomerPhone:      body.CustomerPhone,
		DeliveryAddress:    body.DeliveryAddress,
		Comment:            body.Comment,
		ExpectedTotalMinor: body.ExpectedTotalMinor,
	}, r.Header.Get(HeaderIdempotencyKey))
	if err != nil {
		return toHTTPError(err)
	}
	if result.Duplicate {
		w.Header().Set("Idempotent-Replay", "true")
	}
	httpx.WriteJSON(w, r, http.StatusCreated, toOrderResponse(result.Order))
	return nil
}

func (h *Handler) getOrder(w http.ResponseWriter, r *http.Request) error {
	customer, err := customerID(r)
	if err != nil {
		return err
	}
	orderID, err := pathOrderID(r)
	if err != nil {
		return err
	}
	order, err := h.svc.GetCustomerOrder(r.Context(), customer, orderID)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toOrderResponse(order))
	return nil
}

func (h *Handler) listOrders(w http.ResponseWriter, r *http.Request) error {
	customer, err := customerID(r)
	if err != nil {
		return err
	}
	page, err := httpx.ParsePage(r, defaultPageLimit, maxPageLimit)
	if err != nil {
		return err
	}
	result, err := h.svc.ListCustomerOrders(r.Context(), customer, page.Limit, page.Offset)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toOrderPageResponse(result))
	return nil
}

// cancelOrder withdraws an order the merchant has not decided on yet. The held
// units return asynchronously through the outbox, so the answer describes the
// order, not the stock.
//
// A repeat of a cancellation is answered like any other repeated lifecycle
// move: 200 with the unchanged order and Idempotent-Replay, so a client that
// lost the first response does not read it as a failure.
func (h *Handler) cancelOrder(w http.ResponseWriter, r *http.Request) error {
	customer, err := customerID(r)
	if err != nil {
		return err
	}
	orderID, err := pathOrderID(r)
	if err != nil {
		return err
	}
	result, err := h.svc.CancelOrder(r.Context(), customer, orderID)
	if err != nil {
		return toHTTPError(err)
	}
	writeTransition(w, r, result)
	return nil
}
