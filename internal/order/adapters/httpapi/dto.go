// Package httpapi is the inbound HTTP adapter of order-service. It maps the
// external contract to use cases and back, and translates domain errors into
// the shared HTTP error envelope. It contains no business rules.
package httpapi

import (
	"time"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
)

// draftItemRequest is one requested position. The price is never accepted from
// the client: it is quoted from catalog-service on every call.
type draftItemRequest struct {
	MenuItemID string `json:"menu_item_id"`
	Qty        int32  `json:"qty"`
}

// previewRequest is a basket to recalculate.
type previewRequest struct {
	MerchantID string             `json:"merchant_id"`
	Items      []draftItemRequest `json:"items"`
}

// createRequest is a basket to place, together with the delivery details.
type createRequest struct {
	MerchantID      string             `json:"merchant_id"`
	Items           []draftItemRequest `json:"items"`
	CustomerName    string             `json:"customer_name"`
	CustomerPhone   string             `json:"customer_phone"`
	DeliveryAddress string             `json:"delivery_address"`
	Comment         string             `json:"comment"`
	// ExpectedTotalMinor is the total the customer saw in the preview. When it
	// is present and no longer matches, the order is refused instead of being
	// placed at a price the customer never agreed to.
	ExpectedTotalMinor *int64 `json:"expected_total_minor"`
}

// toLines converts the requested positions. A malformed identifier is reported
// as a validation problem rather than silently dropped.
func toLines(items []draftItemRequest) ([]domain.Line, []string) {
	lines := make([]domain.Line, 0, len(items))
	var bad []string
	for _, item := range items {
		id, err := uuid.Parse(item.MenuItemID)
		if err != nil {
			bad = append(bad, item.MenuItemID)
			continue
		}
		lines = append(lines, domain.Line{MenuItemID: id, Qty: item.Qty})
	}
	return lines, bad
}

type previewLineResponse struct {
	MenuItemID     string `json:"menu_item_id"`
	ExternalID     string `json:"external_id,omitempty"`
	Name           string `json:"name,omitempty"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	Qty            int32  `json:"qty"`
	LineTotalMinor int64  `json:"line_total_minor"`
	AvailableQty   int32  `json:"available_qty"`
	Problem        string `json:"problem"`
}

type previewResponse struct {
	MerchantID       string                `json:"merchant_id"`
	MerchantName     string                `json:"merchant_name"`
	MenuVersion      int64                 `json:"menu_version"`
	Items            []previewLineResponse `json:"items"`
	ItemsTotalMinor  int64                 `json:"items_total_minor"`
	DeliveryFeeMinor int64                 `json:"delivery_fee_minor"`
	TotalMinor       int64                 `json:"total_minor"`
	MinOrderMinor    int64                 `json:"min_order_minor"`
	Currency         string                `json:"currency"`
	ETAMinutes       int32                 `json:"eta_minutes"`
	Orderable        bool                  `json:"orderable"`
	Problems         []string              `json:"problems"`
}

func toPreviewResponse(p domain.Preview) previewResponse {
	problems := make([]string, 0, len(p.Problems))
	for _, problem := range p.Problems {
		problems = append(problems, string(problem))
	}
	return previewResponse{
		MerchantID:       p.MerchantID.String(),
		MerchantName:     p.MerchantName,
		MenuVersion:      p.MenuVersion,
		Items:            toPreviewLines(p.Lines),
		ItemsTotalMinor:  p.ItemsTotalMinor,
		DeliveryFeeMinor: p.DeliveryFeeMinor,
		TotalMinor:       p.TotalMinor,
		MinOrderMinor:    p.MinOrderMinor,
		Currency:         p.Currency,
		ETAMinutes:       p.ETAMinutes,
		Orderable:        p.Orderable(),
		Problems:         problems,
	}
}

func toPreviewLines(lines []domain.PreviewLine) []previewLineResponse {
	out := make([]previewLineResponse, 0, len(lines))
	for _, l := range lines {
		out = append(out, previewLineResponse{
			MenuItemID:     l.MenuItemID.String(),
			ExternalID:     l.ExternalID,
			Name:           l.Name,
			UnitPriceMinor: l.UnitPriceMinor,
			Qty:            l.Qty,
			LineTotalMinor: l.LineTotalMinor,
			AvailableQty:   l.AvailableQty,
			Problem:        string(l.Problem),
		})
	}
	return out
}

type orderItemResponse struct {
	MenuItemID     string `json:"menu_item_id"`
	ExternalID     string `json:"external_id"`
	Name           string `json:"name"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	Qty            int32  `json:"qty"`
	LineTotalMinor int64  `json:"line_total_minor"`
}

// orderResponse is the external view of an order. It carries the snapshot of
// the names and prices taken at placement time, not the current menu.
type orderResponse struct {
	OrderID          string              `json:"order_id"`
	MerchantID       string              `json:"merchant_id"`
	MerchantName     string              `json:"merchant_name"`
	CustomerID       string              `json:"customer_id"`
	Status           string              `json:"status"`
	Items            []orderItemResponse `json:"items"`
	ItemsTotalMinor  int64               `json:"items_total_minor"`
	DeliveryFeeMinor int64               `json:"delivery_fee_minor"`
	TotalMinor       int64               `json:"total_minor"`
	Currency         string              `json:"currency"`
	ETAMinutes       int32               `json:"eta_minutes"`
	RejectReason     string              `json:"reject_reason,omitempty"`
	CustomerName     string              `json:"customer_name"`
	CustomerPhone    string              `json:"customer_phone"`
	DeliveryAddress  string              `json:"delivery_address"`
	Comment          string              `json:"comment,omitempty"`
	CreatedAt        string              `json:"created_at"`
	UpdatedAt        string              `json:"updated_at"`
}

func toOrderResponse(o domain.Order) orderResponse {
	items := make([]orderItemResponse, 0, len(o.Items))
	for _, item := range o.Items {
		items = append(items, orderItemResponse{
			MenuItemID:     item.MenuItemID.String(),
			ExternalID:     item.ExternalID,
			Name:           item.Name,
			UnitPriceMinor: item.UnitPriceMinor,
			Qty:            item.Qty,
			LineTotalMinor: item.LineTotalMinor,
		})
	}
	return orderResponse{
		OrderID:          o.ID.String(),
		MerchantID:       o.MerchantID.String(),
		MerchantName:     o.MerchantName,
		CustomerID:       o.CustomerID.String(),
		Status:           string(o.Status),
		Items:            items,
		ItemsTotalMinor:  o.ItemsTotalMinor,
		DeliveryFeeMinor: o.DeliveryFeeMinor,
		TotalMinor:       o.TotalMinor,
		Currency:         o.Currency,
		ETAMinutes:       o.ETAMinutes,
		RejectReason:     o.RejectReason,
		CustomerName:     o.CustomerName,
		CustomerPhone:    o.CustomerPhone,
		DeliveryAddress:  o.DeliveryAddress,
		Comment:          o.Comment,
		CreatedAt:        o.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        o.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type orderPageResponse struct {
	Items  []orderResponse `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

func toOrderPageResponse(p app.OrderPage) orderPageResponse {
	items := make([]orderResponse, 0, len(p.Items))
	for _, o := range p.Items {
		items = append(items, toOrderResponse(o))
	}
	return orderPageResponse{Items: items, Total: p.Total, Limit: p.Limit, Offset: p.Offset}
}

// acceptRequest carries the merchant's own preparation estimate. Zero means
// "keep the average of the outlet", which the order already carries.
type acceptRequest struct {
	ETAMinutes int32 `json:"eta_minutes"`
}

type rejectRequest struct {
	Reason string `json:"reason"`
}

type statusRequest struct {
	Status string `json:"status"`
}
