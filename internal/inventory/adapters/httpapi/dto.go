// Package httpapi is the inbound HTTP adapter of inventory-service. It maps the
// external contract to use cases and back, and translates domain errors into
// the shared HTTP error envelope. It contains no business rules.
package httpapi

import (
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
)

// stockItemResponse is the external representation of a stock card. Field
// names and types are part of the frozen contract in
// api/openapi/inventory.v1.yaml.
type stockItemResponse struct {
	MenuItemID   string `json:"menu_item_id"`
	SalesState   string `json:"sales_state"`
	OnHandQty    int32  `json:"on_hand_qty"`
	ReservedQty  int32  `json:"reserved_qty"`
	AvailableQty int32  `json:"available_qty"`
	Sellable     bool   `json:"sellable"`
	Version      int64  `json:"version"`
	UpdatedAt    string `json:"updated_at"`
}

func toStockItemResponse(s domain.StockItem) stockItemResponse {
	return stockItemResponse{
		MenuItemID:   s.MenuItemID.String(),
		SalesState:   string(s.SalesState),
		OnHandQty:    s.OnHandQty,
		ReservedQty:  s.ReservedQty,
		AvailableQty: s.AvailableQty(),
		Sellable:     s.Sellable(),
		Version:      s.Version,
		UpdatedAt:    s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type stockPageResponse struct {
	Items  []stockItemResponse `json:"items"`
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

func toStockPageResponse(p app.StockPage) stockPageResponse {
	items := make([]stockItemResponse, 0, len(p.Items))
	for _, s := range p.Items {
		items = append(items, toStockItemResponse(s))
	}
	return stockPageResponse{Items: items, Total: p.Total, Limit: p.Limit, Offset: p.Offset}
}

// availabilityResponse is the public view. It deliberately reports tracked so
// that a caller can tell "the merchant keeps no card for this item" apart from
// "the card exists and holds zero units".
type availabilityResponse struct {
	MerchantID string                     `json:"merchant_id"`
	Items      []availabilityItemResponse `json:"items"`
}

type availabilityItemResponse struct {
	MenuItemID   string `json:"menu_item_id"`
	Tracked      bool   `json:"tracked"`
	SalesState   string `json:"sales_state"`
	AvailableQty int32  `json:"available_qty"`
	Sellable     bool   `json:"sellable"`
}

func toAvailabilityResponse(merchantID string, items []app.AvailabilityItem) availabilityResponse {
	out := make([]availabilityItemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, availabilityItemResponse{
			MenuItemID:   item.MenuItemID.String(),
			Tracked:      item.Tracked,
			SalesState:   string(item.SalesState),
			AvailableQty: item.Available,
			Sellable:     item.Tracked && item.SalesState == domain.SalesEnabled && item.Available > 0,
		})
	}
	return availabilityResponse{MerchantID: merchantID, Items: out}
}

// setOnHandRequest is the absolute assignment of units.
type setOnHandRequest struct {
	OnHandQty *int32 `json:"on_hand_qty"`
	Reason    string `json:"reason"`
}

// adjustRequest is a relative change. OperationKey makes the repeat of a
// non-idempotent operation safe.
type adjustRequest struct {
	Delta        *int32 `json:"delta"`
	OperationKey string `json:"operation_key"`
	Reason       string `json:"reason"`
}

type adjustResponse struct {
	stockItemResponse
	Duplicate bool `json:"duplicate"`
}

// salesStateRequest toggles the stop list. It carries no quantity on purpose:
// the operation must never confirm units.
type salesStateRequest struct {
	SalesState string `json:"sales_state"`
}

type movementResponse struct {
	ID            int64  `json:"id"`
	MenuItemID    string `json:"menu_item_id"`
	Kind          string `json:"kind"`
	DeltaOnHand   int32  `json:"delta_on_hand"`
	DeltaReserved int32  `json:"delta_reserved"`
	OnHandAfter   int32  `json:"on_hand_after"`
	ReservedAfter int32  `json:"reserved_after"`
	ReservationID string `json:"reservation_id,omitempty"`
	OperationKey  string `json:"operation_key,omitempty"`
	Reason        string `json:"reason,omitempty"`
	CreatedAt     string `json:"created_at"`
}

type movementPageResponse struct {
	Items  []movementResponse `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

func toMovementPageResponse(p app.MovementPage) movementPageResponse {
	items := make([]movementResponse, 0, len(p.Items))
	for _, m := range p.Items {
		row := movementResponse{
			ID:            m.ID,
			MenuItemID:    m.MenuItemID.String(),
			Kind:          string(m.Kind),
			DeltaOnHand:   m.DeltaOnHand,
			DeltaReserved: m.DeltaReserved,
			OnHandAfter:   m.OnHandAfter,
			ReservedAfter: m.ReservedAfter,
			OperationKey:  m.OperationKey,
			Reason:        m.Reason,
			CreatedAt:     m.CreatedAt.UTC().Format(time.RFC3339),
		}
		if m.ReservationID != nil {
			row.ReservationID = m.ReservationID.String()
		}
		items = append(items, row)
	}
	return movementPageResponse{Items: items, Total: p.Total, Limit: p.Limit, Offset: p.Offset}
}
