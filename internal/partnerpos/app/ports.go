// Package app holds the use cases of partner-pos: publish the menu of the
// outlet, keep its stock in step with the till and answer incoming orders.
//
// It depends on the domain and on the Platform port declared here. The port is
// deliberately narrow and speaks only the vocabulary of the public HTTP APIs:
// partner-pos is an external system and integrates like any other outlet.
package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

// PublishedItem ties the identifier of the outlet's own system to the platform
// identifier the catalogue assigned.
type PublishedItem struct {
	ExternalID string
	MenuItemID string
}

// OrderItem is one position of an order as the platform reports it.
type OrderItem struct {
	MenuItemID string
	ExternalID string
	Name       string
	Qty        int32
}

// Order is an order of the outlet as the platform reports it. The till keeps
// no orders of its own: everything it needs is in this record.
type Order struct {
	ID         string
	Status     string
	TotalMinor int64
	ETAMinutes int32
	UpdatedAt  string
	Items      []OrderItem
}

// Platform is the port to the running platform. Every method maps to one
// public HTTP call made with the access key of the outlet.
type Platform interface {
	// PublishMenu replaces the published menu with the till menu and returns
	// the new menu version.
	PublishMenu(ctx context.Context, menu domain.Menu) (int64, error)
	// FetchMenu reads the published menu back, which is how the till learns
	// the platform identifier of every position.
	FetchMenu(ctx context.Context) (int64, []PublishedItem, error)
	// SetStock assigns the absolute number of units of one position.
	SetStock(ctx context.Context, menuItemID string, qty int32, reason string) error
	// SetSalesState puts one position on the stop list or takes it off. It
	// never changes quantities.
	SetSalesState(ctx context.Context, menuItemID string, stopped bool) error
	// ListOrders returns the orders of the outlet in the given statuses.
	ListOrders(ctx context.Context, statuses []string, limit int) ([]Order, error)
	// AcceptOrder confirms an order with a preparation estimate.
	AcceptOrder(ctx context.Context, orderID string, etaMinutes int32) error
	// RejectOrder refuses an order with a reason.
	RejectOrder(ctx context.Context, orderID, reason string) error
	// AdvanceOrder moves a confirmed order one step along preparation.
	AdvanceOrder(ctx context.Context, orderID, status string) error
}
