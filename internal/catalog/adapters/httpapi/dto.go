// Package httpapi is the inbound HTTP adapter of catalog-service. It maps the
// external contract to use cases and back, and translates domain errors into
// the shared HTTP error envelope. It contains no business rules.
package httpapi

import (
	"time"

	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
)

// menuItemResponse is the external representation of one menu position. Field
// names and types are part of the frozen contract in
// api/openapi/catalog.v1.yaml.
//
// It carries no quantity: how many units are available belongs to
// inventory-service, and a client combines this menu with the availability
// endpoint of that service.
type menuItemResponse struct {
	MenuItemID  string `json:"menu_item_id"`
	ExternalID  string `json:"external_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency"`
	Position    int32  `json:"position"`
	UpdatedAt   string `json:"updated_at"`
}

func toMenuItemResponse(i domain.MenuItem) menuItemResponse {
	return menuItemResponse{
		MenuItemID:  i.ID.String(),
		ExternalID:  i.ExternalID,
		Name:        i.Name,
		Description: i.Description,
		PriceMinor:  i.PriceMinor,
		Currency:    domain.Currency,
		Position:    i.Position,
		UpdatedAt:   i.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type menuCategoryResponse struct {
	Name     string             `json:"name"`
	Position int32              `json:"position"`
	Items    []menuItemResponse `json:"items"`
}

type menuResponse struct {
	MerchantID  string                 `json:"merchant_id"`
	MenuVersion int64                  `json:"menu_version"`
	Currency    string                 `json:"currency"`
	Categories  []menuCategoryResponse `json:"categories"`
}

func toMenuResponse(m domain.Menu) menuResponse {
	categories := make([]menuCategoryResponse, 0, len(m.Categories))
	for _, c := range m.Categories {
		items := make([]menuItemResponse, 0, len(c.Items))
		for _, item := range c.Items {
			items = append(items, toMenuItemResponse(item))
		}
		categories = append(categories, menuCategoryResponse{
			Name:     c.Category.Name,
			Position: c.Category.Position,
			Items:    items,
		})
	}
	return menuResponse{
		MerchantID:  m.MerchantID.String(),
		MenuVersion: m.MenuVersion,
		Currency:    domain.Currency,
		Categories:  categories,
	}
}

// publishItemRequest is one item of a published snapshot. The merchant
// addresses it by the identifier of its own system, never by the platform id.
type publishItemRequest struct {
	ExternalID  string `json:"external_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Position    int32  `json:"position"`
}

type publishCategoryRequest struct {
	Name     string               `json:"name"`
	Position int32                `json:"position"`
	Items    []publishItemRequest `json:"items"`
}

// publishMenuRequest is a whole menu. Publication replaces the menu with it:
// positions absent from the snapshot are withdrawn from sale.
type publishMenuRequest struct {
	Categories []publishCategoryRequest `json:"categories"`
}

func (r publishMenuRequest) toSnapshot() domain.MenuSnapshot {
	categories := make([]domain.SnapshotCategory, 0, len(r.Categories))
	for _, c := range r.Categories {
		items := make([]domain.SnapshotItem, 0, len(c.Items))
		for _, item := range c.Items {
			items = append(items, domain.SnapshotItem{
				ExternalID:  item.ExternalID,
				Name:        item.Name,
				Description: item.Description,
				PriceMinor:  item.PriceMinor,
				Position:    item.Position,
			})
		}
		categories = append(categories, domain.SnapshotCategory{
			Name:     c.Name,
			Position: c.Position,
			Items:    items,
		})
	}
	return domain.MenuSnapshot{Categories: categories}
}

// publishResponse reports the published menu together with what the
// publication actually changed, so a merchant integration can see whether its
// export did what it intended.
type publishResponse struct {
	menuResponse
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Withdrawn int `json:"withdrawn"`
}

func toPublishResponse(r app.PublishResult) publishResponse {
	return publishResponse{
		menuResponse: toMenuResponse(r.Menu),
		Created:      r.Created,
		Updated:      r.Updated,
		Withdrawn:    r.Withdrawn,
	}
}

// patchItemRequest changes the name or the price of one published item. A
// nil field means "leave unchanged", which is how an absent field is told
// apart from an empty value.
type patchItemRequest struct {
	Name       *string `json:"name"`
	PriceMinor *int64  `json:"price_minor"`
}

func (r patchItemRequest) toPatch() domain.ItemPatch {
	return domain.ItemPatch{Name: r.Name, PriceMinor: r.PriceMinor}
}

type patchItemResponse struct {
	menuItemResponse
	MenuVersion int64 `json:"menu_version"`
}

func toPatchItemResponse(r app.UpdateItemResult) patchItemResponse {
	return patchItemResponse{
		menuItemResponse: toMenuItemResponse(r.Item),
		MenuVersion:      r.MenuVersion,
	}
}
