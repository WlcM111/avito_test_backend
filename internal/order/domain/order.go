package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Line is one requested position of a draft: a menu item and a number of
// units. The price is never taken from the client - it is quoted from
// catalog-service at preview and again at placement.
type Line struct {
	MenuItemID uuid.UUID
	Qty        int32
}

// Draft is a customer's intent to order, before anything is quoted or held.
type Draft struct {
	MerchantID      uuid.UUID
	CustomerID      uuid.UUID
	Lines           []Line
	CustomerName    string
	CustomerPhone   string
	DeliveryAddress string
	Comment         string
	// ExpectedTotalMinor, when not nil, is the total the customer saw in the
	// preview. A different recalculated total aborts the placement instead of
	// charging a price the customer never agreed to.
	ExpectedTotalMinor *int64
}

// NormalizeLines collapses duplicate positions by summing their quantities and
// returns the lines sorted by menu item id. The order is deterministic because
// inventory-service holds units in that order, and a stable order on our side
// keeps the reservation reproducible.
func NormalizeLines(lines []Line) []Line {
	if len(lines) == 0 {
		return nil
	}
	merged := make(map[uuid.UUID]int32, len(lines))
	order := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		if _, seen := merged[l.MenuItemID]; !seen {
			order = append(order, l.MenuItemID)
		}
		merged[l.MenuItemID] += l.Qty
	}
	sort.Slice(order, func(i, j int) bool {
		return order[i].String() < order[j].String()
	})
	out := make([]Line, 0, len(order))
	for _, id := range order {
		out = append(out, Line{MenuItemID: id, Qty: merged[id]})
	}
	return out
}

// ValidateLines checks the requested positions against the business bounds.
// It expects the lines to be already normalized.
func ValidateLines(lines []Line) error {
	ve := &ValidationError{}
	switch n := len(lines); {
	case n == 0:
		ve.Add("items", "must contain at least one position")
	case n > MaxOrderLines:
		ve.Add("items", "must not contain more than 50 positions")
	}
	var units int32
	for _, l := range lines {
		if l.MenuItemID == uuid.Nil {
			ve.Add("items.menu_item_id", "is required")
		}
		if l.Qty < MinLineQty || l.Qty > MaxLineQty {
			ve.Add("items.qty", "must be between 1 and 99")
		}
		units += l.Qty
	}
	if units > MaxOrderUnits {
		ve.Add("items", "must not request more than 200 units in total")
	}
	return ve.OrNil()
}

// Validate checks the whole draft. Contact details are required because the
// order has to be deliverable; the platform performs no customer
// authentication, so there is nothing else to fall back on.
func (d Draft) Validate() error {
	ve := &ValidationError{}
	if d.MerchantID == uuid.Nil {
		ve.Add("merchant_id", "is required")
	}
	if d.CustomerID == uuid.Nil {
		ve.Add("X-Customer-Id", "is required")
	}
	validateText(ve, "customer_name", d.CustomerName, 1, 200)
	validateText(ve, "customer_phone", d.CustomerPhone, 5, 32)
	validateText(ve, "delivery_address", d.DeliveryAddress, 1, 500)
	if len([]rune(d.Comment)) > MaxCommentLength {
		ve.Add("comment", "must not exceed 500 characters")
	}
	if d.ExpectedTotalMinor != nil && *d.ExpectedTotalMinor <= 0 {
		ve.Add("expected_total_minor", "must be greater than zero")
	}
	if err := ValidateLines(d.Lines); err != nil {
		if lineErr, ok := AsValidationError(err); ok {
			ve.Violations = append(ve.Violations, lineErr.Violations...)
		}
	}
	return ve.OrNil()
}

func validateText(ve *ValidationError, field, value string, minLen, maxLen int) {
	n := len([]rune(strings.TrimSpace(value)))
	switch {
	case n == 0:
		ve.Add(field, "is required")
	case n < minLen:
		ve.Add(field, "is too short")
	case n > maxLen:
		ve.Add(field, "is too long")
	}
}

// Item is one position of a placed order: a snapshot of the name and the price
// at placement time. Reading an order therefore never calls catalog-service,
// and a later price change never rewrites history.
type Item struct {
	MenuItemID     uuid.UUID
	ExternalID     string
	Name           string
	UnitPriceMinor int64
	Qty            int32
	LineTotalMinor int64
}

// Order is the aggregate: the order, its positions and its lifecycle.
type Order struct {
	ID              uuid.UUID
	MerchantID      uuid.UUID
	MerchantName    string
	CustomerID      uuid.UUID
	IdempotencyKey  string
	CustomerName    string
	CustomerPhone   string
	DeliveryAddress string
	Comment         string
	Status          Status
	Items           []Item

	ItemsTotalMinor  int64
	DeliveryFeeMinor int64
	TotalMinor       int64
	Currency         string

	ETAMinutes   int32
	RejectReason string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ItemProblem explains why one requested position cannot be ordered. The
// values combine what catalog-service says about the position and what
// inventory-service says about its units, because the customer does not care
// which service refused.
type ItemProblem string

const (
	// ItemProblemNone means the position can be ordered.
	ItemProblemNone ItemProblem = "NONE"
	// ItemProblemNotFound means the catalogue has no such position.
	ItemProblemNotFound ItemProblem = "ITEM_NOT_FOUND"
	// ItemProblemNotInMerchant means the position belongs to another merchant.
	ItemProblemNotInMerchant ItemProblem = "ITEM_NOT_IN_MERCHANT"
	// ItemProblemUnpublished means the position was withdrawn from the menu.
	ItemProblemUnpublished ItemProblem = "ITEM_UNPUBLISHED"
	// ItemProblemNotTracked means the merchant keeps no stock card for the
	// position, so no quantity is confirmed for it.
	ItemProblemNotTracked ItemProblem = "ITEM_NOT_TRACKED"
	// ItemProblemSalesStopped means the position is on the merchant stop list.
	ItemProblemSalesStopped ItemProblem = "ITEM_SALES_STOPPED"
	// ItemProblemInsufficientUnits means fewer units are available than asked.
	ItemProblemInsufficientUnits ItemProblem = "INSUFFICIENT_UNITS"
)

// OrderProblem explains why the order as a whole cannot be placed.
type OrderProblem string

const (
	// OrderProblemMerchantNotFound means the merchant does not exist.
	OrderProblemMerchantNotFound OrderProblem = "MERCHANT_NOT_FOUND"
	// OrderProblemMerchantSuspended means the operator disconnected the outlet.
	OrderProblemMerchantSuspended OrderProblem = "MERCHANT_SUSPENDED"
	// OrderProblemNotAcceptingOrders means the outlet paused order intake.
	OrderProblemNotAcceptingOrders OrderProblem = "MERCHANT_NOT_ACCEPTING_ORDERS"
	// OrderProblemBelowMinOrder means the positions total is below the minimum
	// the outlet accepts. Delivery is not counted towards it.
	OrderProblemBelowMinOrder OrderProblem = "MIN_ORDER_NOT_MET"
)

// PreviewLine is the recalculated state of one requested position.
type PreviewLine struct {
	MenuItemID     uuid.UUID
	ExternalID     string
	Name           string
	UnitPriceMinor int64
	Qty            int32
	LineTotalMinor int64
	AvailableQty   int32
	Problem        ItemProblem
}

// Preview is the answer of the preview use case: recalculated amounts plus the
// list of problems per position and for the order as a whole.
type Preview struct {
	MerchantID       uuid.UUID
	MerchantName     string
	MenuVersion      int64
	Lines            []PreviewLine
	ItemsTotalMinor  int64
	DeliveryFeeMinor int64
	TotalMinor       int64
	MinOrderMinor    int64
	Currency         string
	ETAMinutes       int32
	Problems         []OrderProblem
}

// Orderable reports whether the draft would be accepted right now.
func (p Preview) Orderable() bool {
	if len(p.Problems) > 0 {
		return false
	}
	for _, l := range p.Lines {
		if l.Problem != ItemProblemNone {
			return false
		}
	}
	return len(p.Lines) > 0
}

// Items converts the previewed positions into the snapshot stored with the
// order. It must only be called for an orderable preview.
func (p Preview) Items() []Item {
	out := make([]Item, 0, len(p.Lines))
	for _, l := range p.Lines {
		out = append(out, Item{
			MenuItemID:     l.MenuItemID,
			ExternalID:     l.ExternalID,
			Name:           l.Name,
			UnitPriceMinor: l.UnitPriceMinor,
			Qty:            l.Qty,
			LineTotalMinor: l.LineTotalMinor,
		})
	}
	return out
}

// ReserveLines converts the previewed positions into the lines that
// inventory-service holds.
func (p Preview) ReserveLines() []Line {
	out := make([]Line, 0, len(p.Lines))
	for _, l := range p.Lines {
		out = append(out, Line{MenuItemID: l.MenuItemID, Qty: l.Qty})
	}
	return out
}
