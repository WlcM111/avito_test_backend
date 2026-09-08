// Package testsupport provides an in-memory implementation of the Platform
// port. It exists so that the use cases of partner-pos can be tested without a
// running platform. It is used by tests only.
package testsupport

import (
	"context"
	"sync"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/app"
	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

// Fixed identifiers of the platform seed, so a test here describes the same
// world as the local stack.
const (
	MenuItemSellable     = "a1111111-1111-4111-8111-111111111111"
	MenuItemEnabledEmpty = "a1111111-1111-4111-8111-222222222222"
	MenuItemStopped      = "a1111111-1111-4111-8111-333333333333"
)

// Stock is what the fake platform knows about one position.
type Stock struct {
	Qty     int32
	Stopped bool
	Reason  string
}

// Platform is an in-memory app.Platform.
type Platform struct {
	mu sync.Mutex

	MenuVersion int64
	Published   []app.PublishedItem
	Stocks      map[string]Stock
	Orders      []app.Order

	// Accepted, Rejected and Advanced record what the till asked the platform
	// to do, so a test can prove the effect happened exactly once.
	Accepted map[string]int32
	Rejected map[string]string
	Advanced map[string][]string

	// PublishErr and friends let a test simulate an unavailable platform.
	PublishErr error
	FetchErr   error
	StockErr   error
	ListErr    error
	DecideErr  error
}

// NewPlatform returns a platform that already knows the three seeded positions.
func NewPlatform() *Platform {
	return &Platform{
		MenuVersion: 1,
		Published: []app.PublishedItem{
			{ExternalID: "POS-001", MenuItemID: MenuItemSellable},
			{ExternalID: "POS-002", MenuItemID: MenuItemEnabledEmpty},
			{ExternalID: "POS-003", MenuItemID: MenuItemStopped},
		},
		Stocks:   map[string]Stock{},
		Accepted: map[string]int32{},
		Rejected: map[string]string{},
		Advanced: map[string][]string{},
	}
}

// SetOrders replaces the queue the fake platform reports.
func (p *Platform) SetOrders(orders ...app.Order) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Orders = orders
}

// Stock returns what the platform stores for one position.
func (p *Platform) Stock(menuItemID string) (Stock, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.Stocks[menuItemID]
	return s, ok
}

// PublishMenu records the published menu.
func (p *Platform) PublishMenu(_ context.Context, menu domain.Menu) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.PublishErr != nil {
		return 0, p.PublishErr
	}
	if len(menu.Items) == 0 {
		return 0, app.Unavailable("catalog publish menu", context.DeadlineExceeded)
	}
	p.MenuVersion++
	return p.MenuVersion, nil
}

// FetchMenu returns the identifier mapping.
func (p *Platform) FetchMenu(_ context.Context) (int64, []app.PublishedItem, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.FetchErr != nil {
		return 0, nil, p.FetchErr
	}
	out := make([]app.PublishedItem, len(p.Published))
	copy(out, p.Published)
	return p.MenuVersion, out, nil
}

// SetStock assigns the units of one position.
func (p *Platform) SetStock(_ context.Context, menuItemID string, qty int32, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.StockErr != nil {
		return p.StockErr
	}
	s := p.Stocks[menuItemID]
	s.Qty, s.Reason = qty, reason
	p.Stocks[menuItemID] = s
	return nil
}

// SetSalesState toggles the stop list without touching the quantity.
func (p *Platform) SetSalesState(_ context.Context, menuItemID string, stopped bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.StockErr != nil {
		return p.StockErr
	}
	s := p.Stocks[menuItemID]
	s.Stopped = stopped
	p.Stocks[menuItemID] = s
	return nil
}

// ListOrders returns the queue filtered by status.
func (p *Platform) ListOrders(_ context.Context, statuses []string, limit int) ([]app.Order, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ListErr != nil {
		return nil, p.ListErr
	}
	allowed := map[string]struct{}{}
	for _, s := range statuses {
		allowed[s] = struct{}{}
	}
	out := make([]app.Order, 0, len(p.Orders))
	for _, o := range p.Orders {
		if _, ok := allowed[o.Status]; len(allowed) > 0 && !ok {
			continue
		}
		out = append(out, o)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// AcceptOrder records a confirmation and moves the order in the fake queue.
func (p *Platform) AcceptOrder(_ context.Context, orderID string, etaMinutes int32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.DecideErr != nil {
		return p.DecideErr
	}
	p.Accepted[orderID] = etaMinutes
	p.moveLocked(orderID, domain.StatusConfirmed)
	return nil
}

// RejectOrder records a refusal.
func (p *Platform) RejectOrder(_ context.Context, orderID, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.DecideErr != nil {
		return p.DecideErr
	}
	p.Rejected[orderID] = reason
	p.moveLocked(orderID, domain.StatusRejected)
	return nil
}

// AdvanceOrder records a preparation step.
func (p *Platform) AdvanceOrder(_ context.Context, orderID, status string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.DecideErr != nil {
		return p.DecideErr
	}
	p.Advanced[orderID] = append(p.Advanced[orderID], status)
	p.moveLocked(orderID, status)
	return nil
}

func (p *Platform) moveLocked(orderID, status string) {
	for i := range p.Orders {
		if p.Orders[i].ID == orderID {
			p.Orders[i].Status = status
			p.Orders[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			return
		}
	}
}

var _ app.Platform = (*Platform)(nil)
