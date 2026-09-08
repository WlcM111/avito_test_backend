// Package domain holds the business entities and rules of partner-pos: the
// menu the outlet keeps in its own till and the rule by which the till answers
// incoming orders.
//
// partner-pos models an EXTERNAL organisation (TZ v3.0 §3.3). It owns no
// platform data, has no database and never reaches the internal gRPC network:
// everything it does goes through the public HTTP APIs with the access key of
// the outlet, exactly like any real point-of-sale integration.
package domain

import (
	"sort"
	"strings"
)

// Bounds of the till menu. They mirror the limits of catalog-service, so a
// menu that the till accepts can always be published.
const (
	MaxExternalIDLength  = 64
	MaxNameLength        = 200
	MaxDescriptionLength = 1000
	MaxCategoryLength    = 100
	MaxPriceMinor        = 100_000_000
	MaxItems             = 500
	MaxOnHandQty         = 100_000
)

// Item is one position of the till menu. It carries both identifiers: the one
// the outlet uses in its own system and the one the platform assigned after
// publication.
type Item struct {
	ExternalID  string
	Name        string
	Description string
	Category    string
	PriceMinor  int64
	Position    int32
	// OnHandQty and SalesStopped are what the till believes about its own
	// kitchen. Synchronising them to inventory-service is a separate step:
	// publishing a menu never creates stock.
	OnHandQty    int32
	SalesStopped bool
	// MenuItemID is the platform identifier. It is empty until the menu has
	// been published and read back.
	MenuItemID string
}

// Menu is the whole till menu.
type Menu struct {
	Items []Item
}

// Category groups the items of one section for publication.
type Category struct {
	Name     string
	Position int32
	Items    []Item
}

// DefaultMenu is the demonstration menu of the till. The external identifiers
// are the ones the platform seed already knows (POS-001…POS-003), so the first
// publication updates the seeded positions instead of creating duplicates and
// the stock cards of inventory-service keep pointing at the same items.
func DefaultMenu() Menu {
	return Menu{Items: []Item{
		{
			ExternalID: "POS-001", Name: "Борщ с говядиной",
			Description: "Классический борщ со сметаной и пампушкой.",
			Category:    "Горячее", PriceMinor: 42000, Position: 0, OnHandQty: 10,
		},
		{
			ExternalID: "POS-002", Name: "Котлета по-киевски",
			Description: "Подаётся с картофельным пюре.",
			Category:    "Горячее", PriceMinor: 56000, Position: 1, OnHandQty: 4,
		},
		{
			ExternalID: "POS-003", Name: "Морс клюквенный",
			Description: "Домашний морс, 400 мл.",
			Category:    "Напитки", PriceMinor: 15000, Position: 0, OnHandQty: 20,
		},
	}}
}

// Find returns the item with that external identifier.
func (m Menu) Find(externalID string) (Item, bool) {
	for _, item := range m.Items {
		if item.ExternalID == externalID {
			return item, true
		}
	}
	return Item{}, false
}

// Categories groups the items for publication: sections ordered by the
// position of their first item, items ordered inside a section. The order is
// deterministic so that two publications of an unchanged menu are identical.
func (m Menu) Categories() []Category {
	byName := map[string]*Category{}
	var order []string
	for _, item := range m.Items {
		c, ok := byName[item.Category]
		if !ok {
			byName[item.Category] = &Category{Name: item.Category, Position: int32(len(order))}
			order = append(order, item.Category)
			c = byName[item.Category]
		}
		c.Items = append(c.Items, item)
	}
	out := make([]Category, 0, len(order))
	for _, name := range order {
		c := byName[name]
		sort.SliceStable(c.Items, func(i, j int) bool {
			if c.Items[i].Position != c.Items[j].Position {
				return c.Items[i].Position < c.Items[j].Position
			}
			return c.Items[i].ExternalID < c.Items[j].ExternalID
		})
		out = append(out, *c)
	}
	return out
}

// Validate checks the menu against the bounds catalog-service enforces. It
// reports every problem at once.
func (m Menu) Validate() error {
	ve := &ValidationError{}
	switch n := len(m.Items); {
	case n == 0:
		ve.Add("items", "must contain at least one position")
	case n > MaxItems:
		ve.Add("items", "must not contain more than 500 positions")
	}
	seen := map[string]struct{}{}
	for _, item := range m.Items {
		validateExternalID(ve, item.ExternalID, seen)
		validateName(ve, item.Name)
		if len([]rune(item.Description)) > MaxDescriptionLength {
			ve.Add("items.description", "must not exceed 1000 characters")
		}
		if n := len([]rune(strings.TrimSpace(item.Category))); n == 0 || n > MaxCategoryLength {
			ve.Add("items.category", "is required and must not exceed 100 characters")
		}
		validatePrice(ve, item.PriceMinor)
		if item.Position < 0 {
			ve.Add("items.position", "must not be negative")
		}
		validateQty(ve, item.OnHandQty)
	}
	return ve.OrNil()
}

func validateExternalID(ve *ValidationError, id string, seen map[string]struct{}) {
	switch n := len(id); {
	case n == 0:
		ve.Add("items.external_id", "is required")
	case n > MaxExternalIDLength:
		ve.Add("items.external_id", "must not exceed 64 characters")
	default:
		if _, dup := seen[id]; dup {
			ve.Add("items.external_id", "must be unique within the menu: "+id)
		}
		seen[id] = struct{}{}
	}
}

func validateName(ve *ValidationError, name string) {
	switch n := len([]rune(strings.TrimSpace(name))); {
	case n == 0:
		ve.Add("items.name", "is required")
	case n > MaxNameLength:
		ve.Add("items.name", "must not exceed 200 characters")
	}
}

func validatePrice(ve *ValidationError, price int64) {
	switch {
	case price <= 0:
		ve.Add("items.price_minor", "must be greater than zero")
	case price > MaxPriceMinor:
		ve.Add("items.price_minor", "must not exceed 100000000 minor units")
	}
}

func validateQty(ve *ValidationError, qty int32) {
	switch {
	case qty < 0:
		ve.Add("items.on_hand_qty", "must not be negative")
	case qty > MaxOnHandQty:
		ve.Add("items.on_hand_qty", "must not exceed 100000")
	}
}

// ItemPatch is a partial change of one till position. A nil field leaves the
// stored value untouched, which is how "absent" is told apart from "empty".
type ItemPatch struct {
	Name         *string
	Description  *string
	PriceMinor   *int64
	OnHandQty    *int32
	SalesStopped *bool
}

// IsEmpty reports whether the patch would change nothing.
func (p ItemPatch) IsEmpty() bool {
	return p.Name == nil && p.Description == nil && p.PriceMinor == nil &&
		p.OnHandQty == nil && p.SalesStopped == nil
}

// Validate checks the patch against the same bounds as the menu.
func (p ItemPatch) Validate() error {
	ve := &ValidationError{}
	if p.IsEmpty() {
		ve.Add("body", "must change at least one field")
	}
	if p.Name != nil {
		validateName(ve, *p.Name)
	}
	if p.Description != nil && len([]rune(*p.Description)) > MaxDescriptionLength {
		ve.Add("items.description", "must not exceed 1000 characters")
	}
	if p.PriceMinor != nil {
		validatePrice(ve, *p.PriceMinor)
	}
	if p.OnHandQty != nil {
		validateQty(ve, *p.OnHandQty)
	}
	return ve.OrNil()
}

// Apply changes one position of the menu in place.
func (m *Menu) Apply(externalID string, patch ItemPatch) (Item, error) {
	for i := range m.Items {
		if m.Items[i].ExternalID != externalID {
			continue
		}
		if patch.Name != nil {
			m.Items[i].Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Description != nil {
			m.Items[i].Description = strings.TrimSpace(*patch.Description)
		}
		if patch.PriceMinor != nil {
			m.Items[i].PriceMinor = *patch.PriceMinor
		}
		if patch.OnHandQty != nil {
			m.Items[i].OnHandQty = *patch.OnHandQty
		}
		if patch.SalesStopped != nil {
			m.Items[i].SalesStopped = *patch.SalesStopped
		}
		return m.Items[i], nil
	}
	return Item{}, ErrItemNotFound
}

// Clone returns a deep copy, so that a caller reading the menu cannot mutate
// the one the till keeps.
func (m Menu) Clone() Menu {
	out := Menu{Items: make([]Item, len(m.Items))}
	copy(out.Items, m.Items)
	return out
}
