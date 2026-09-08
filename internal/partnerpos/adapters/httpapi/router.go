// Package httpapi is the inbound HTTP adapter of partner-pos. It exposes the
// control API of the demonstration till (TZ v3.0 §7.2) and translates domain
// errors into the shared error envelope. It holds no business rules.
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/avito-kitchen/platform/internal/partnerpos/app"
	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
)

// Domain-specific error codes of the control API.
const (
	codeItemNotFound = "POS_ITEM_NOT_FOUND"
	codeNotPublished = "POS_MENU_NOT_PUBLISHED"
)

// Handler serves the control API of the till.
type Handler struct {
	svc    *app.Service
	logger *slog.Logger
}

// NewHandler wires the HTTP adapter to the use cases.
func NewHandler(svc *app.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Routes returns the router of the control API. Paths and methods are fixed by
// api/openapi/partner-pos.v1.yaml.
//
// The API is unauthenticated on purpose: it controls a demonstration till that
// runs next to the platform in the same compose stack, and it exposes no
// platform data of its own. The access key of the outlet never leaves the
// process - it is used only for the outbound calls.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pos/menu", h.handle(h.getMenu))
	mux.HandleFunc("PATCH /pos/menu/{external_id}", h.handle(h.patchItem))
	mux.HandleFunc("POST /pos/publish", h.handle(h.publish))
	mux.HandleFunc("GET /pos/orders", h.handle(h.getOrders))
	mux.HandleFunc("PUT /pos/behavior", h.handle(h.setBehavior))
	mux.HandleFunc("/", h.handle(h.notFound))
	return mux
}

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

// ------------------------------------------------------------------ payloads

type itemResponse struct {
	ExternalID   string `json:"external_id"`
	MenuItemID   string `json:"menu_item_id,omitempty"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Category     string `json:"category"`
	PriceMinor   int64  `json:"price_minor"`
	Position     int32  `json:"position"`
	OnHandQty    int32  `json:"on_hand_qty"`
	SalesStopped bool   `json:"sales_stopped"`
}

func toItemResponse(i domain.Item) itemResponse {
	return itemResponse{
		ExternalID: i.ExternalID, MenuItemID: i.MenuItemID, Name: i.Name,
		Description: i.Description, Category: i.Category, PriceMinor: i.PriceMinor,
		Position: i.Position, OnHandQty: i.OnHandQty, SalesStopped: i.SalesStopped,
	}
}

type menuResponse struct {
	Published   bool           `json:"published"`
	MenuVersion int64          `json:"menu_version"`
	Currency    string         `json:"currency"`
	Items       []itemResponse `json:"items"`
}

type patchItemRequest struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	PriceMinor   *int64  `json:"price_minor"`
	OnHandQty    *int32  `json:"on_hand_qty"`
	SalesStopped *bool   `json:"sales_stopped"`
}

type behaviorPayload struct {
	Mode         string `json:"mode"`
	ETAMinutes   int32  `json:"eta_minutes"`
	RejectReason string `json:"reject_reason"`
	Kitchen      bool   `json:"kitchen"`
}

type publishResponse struct {
	MenuVersion int64 `json:"menu_version"`
	Items       int   `json:"items"`
	Matched     int   `json:"matched"`
}

type orderResponse struct {
	OrderID    string `json:"order_id"`
	Status     string `json:"status"`
	TotalMinor int64  `json:"total_minor"`
	ETAMinutes int32  `json:"eta_minutes"`
	UpdatedAt  string `json:"updated_at"`
	Items      []struct {
		MenuItemID string `json:"menu_item_id"`
		ExternalID string `json:"external_id"`
		Name       string `json:"name"`
		Qty        int32  `json:"qty"`
	} `json:"items"`
}

type ordersResponse struct {
	Items []orderResponse `json:"items"`
	Total int             `json:"total"`
}

// ------------------------------------------------------------------ handlers

func (h *Handler) getMenu(w http.ResponseWriter, r *http.Request) error {
	state := h.svc.Menu()
	items := make([]itemResponse, 0, len(state.Menu.Items))
	for _, item := range state.Menu.Items {
		items = append(items, toItemResponse(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, menuResponse{
		Published: state.Published, MenuVersion: state.MenuVersion,
		Currency: "RUB", Items: items,
	})
	return nil
}

func (h *Handler) patchItem(w http.ResponseWriter, r *http.Request) error {
	externalID := r.PathValue("external_id")
	if externalID == "" {
		return httpx.Unprocessable("external_id is required")
	}
	var body patchItemRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	item, err := h.svc.PatchItem(externalID, domain.ItemPatch{
		Name: body.Name, Description: body.Description, PriceMinor: body.PriceMinor,
		OnHandQty: body.OnHandQty, SalesStopped: body.SalesStopped,
	})
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, toItemResponse(item))
	return nil
}

// publish sends the till menu to the catalogue and immediately pushes the
// quantities, because a menu without stock sells nothing.
func (h *Handler) publish(w http.ResponseWriter, r *http.Request) error {
	result, err := h.svc.PublishMenu(r.Context())
	if err != nil {
		return toHTTPError(err)
	}
	if _, err := h.svc.SyncStock(r.Context()); err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, publishResponse{
		MenuVersion: result.MenuVersion, Items: result.Items, Matched: result.Matched,
	})
	return nil
}

func (h *Handler) getOrders(w http.ResponseWriter, r *http.Request) error {
	orders, err := h.svc.Orders(r.Context())
	if err != nil {
		return toHTTPError(err)
	}
	items := make([]orderResponse, 0, len(orders))
	for _, o := range orders {
		out := orderResponse{
			OrderID: o.ID, Status: o.Status, TotalMinor: o.TotalMinor,
			ETAMinutes: o.ETAMinutes, UpdatedAt: o.UpdatedAt,
		}
		for _, item := range o.Items {
			out.Items = append(out.Items, struct {
				MenuItemID string `json:"menu_item_id"`
				ExternalID string `json:"external_id"`
				Name       string `json:"name"`
				Qty        int32  `json:"qty"`
			}{item.MenuItemID, item.ExternalID, item.Name, item.Qty})
		}
		items = append(items, out)
	}
	httpx.WriteJSON(w, r, http.StatusOK, ordersResponse{Items: items, Total: len(items)})
	return nil
}

func (h *Handler) setBehavior(w http.ResponseWriter, r *http.Request) error {
	var body behaviorPayload
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	behavior, err := h.svc.SetBehavior(domain.Behavior{
		Mode: domain.Mode(body.Mode), ETAMinutes: body.ETAMinutes,
		RejectReason: body.RejectReason, Kitchen: body.Kitchen,
	})
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, r, http.StatusOK, behaviorPayload{
		Mode: string(behavior.Mode), ETAMinutes: behavior.ETAMinutes,
		RejectReason: behavior.RejectReason, Kitchen: behavior.Kitchen,
	})
	return nil
}

// toHTTPError maps a domain or application error onto the shared envelope.
func toHTTPError(err error) error {
	switch {
	case errors.Is(err, domain.ErrItemNotFound):
		return httpx.NewError(http.StatusNotFound, codeItemNotFound,
			"the till has no position with this external identifier")
	case errors.Is(err, domain.ErrNotPublished):
		return httpx.Conflict(codeNotPublished,
			"publish the menu first: the till does not know the platform identifiers yet")
	case errors.Is(err, app.ErrPlatformUnavailable):
		return httpx.Unavailable("the platform is unavailable", err)
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
