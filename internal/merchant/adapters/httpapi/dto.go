// Package httpapi is the inbound HTTP adapter of merchant-service. It maps the
// external contract to use cases and back, and translates domain errors into
// the shared HTTP error envelope. It contains no business rules.
package httpapi

import (
	"time"

	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/domain"
)

// merchantResponse is the external representation of a merchant. Field names
// and types are part of the frozen contract in api/openapi/merchant.v1.yaml.
type merchantResponse struct {
	ID                string `json:"id"`
	Slug              string `json:"slug"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Cuisine           string `json:"cuisine"`
	City              string `json:"city"`
	Address           string `json:"address"`
	Phone             string `json:"phone"`
	Status            string `json:"status"`
	IsAcceptingOrders bool   `json:"is_accepting_orders"`
	CanAcceptOrders   bool   `json:"can_accept_orders"`
	MinOrderMinor     int64  `json:"min_order_minor"`
	DeliveryFeeMinor  int64  `json:"delivery_fee_minor"`
	AvgPrepMinutes    int32  `json:"avg_prep_minutes"`
	Currency          string `json:"currency"`
	Version           int64  `json:"version"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// currencyCode is fixed for the MVP: all amounts are RUB minor units.
const currencyCode = "RUB"

func toMerchantResponse(m domain.Merchant) merchantResponse {
	return merchantResponse{
		ID:                m.ID.String(),
		Slug:              m.Slug,
		Name:              m.Name,
		Description:       m.Description,
		Cuisine:           m.Cuisine,
		City:              m.City,
		Address:           m.Address,
		Phone:             m.Phone,
		Status:            string(m.Status),
		IsAcceptingOrders: m.IsAcceptingOrders,
		CanAcceptOrders:   m.CanAcceptOrders(),
		MinOrderMinor:     m.MinOrderMinor,
		DeliveryFeeMinor:  m.DeliveryFeeMinor,
		AvgPrepMinutes:    m.AvgPrepMinutes,
		Currency:          currencyCode,
		Version:           m.Version,
		CreatedAt:         m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         m.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// listResponse is the paginated envelope of the public merchant list.
type listResponse struct {
	Items  []merchantResponse `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

func toListResponse(p app.ListPage) listResponse {
	items := make([]merchantResponse, 0, len(p.Items))
	for _, m := range p.Items {
		items = append(items, toMerchantResponse(m))
	}
	return listResponse{Items: items, Total: p.Total, Limit: p.Limit, Offset: p.Offset}
}

// patchProfileRequest is a partial update. A field left out of the JSON body
// stays nil and is not modified.
type patchProfileRequest struct {
	IsAcceptingOrders *bool  `json:"is_accepting_orders"`
	MinOrderMinor     *int64 `json:"min_order_minor"`
	DeliveryFeeMinor  *int64 `json:"delivery_fee_minor"`
	AvgPrepMinutes    *int32 `json:"avg_prep_minutes"`
}

func (r patchProfileRequest) toPatch() domain.ProfilePatch {
	return domain.ProfilePatch{
		IsAcceptingOrders: r.IsAcceptingOrders,
		MinOrderMinor:     r.MinOrderMinor,
		DeliveryFeeMinor:  r.DeliveryFeeMinor,
		AvgPrepMinutes:    r.AvgPrepMinutes,
	}
}
