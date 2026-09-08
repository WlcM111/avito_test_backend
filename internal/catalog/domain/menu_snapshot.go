package domain

import (
	"sort"
	"strings"

	"github.com/google/uuid"
)

// SnapshotItem is one item of a menu a merchant publishes. It is addressed by
// the identifier of the merchant's own system, never by the platform id: the
// merchant does not know the platform id of a position it has just created.
type SnapshotItem struct {
	ExternalID  string
	Name        string
	Description string
	PriceMinor  int64
	Position    int32
}

// SnapshotCategory is one section of a published menu together with its items.
type SnapshotCategory struct {
	Name     string
	Position int32
	Items    []SnapshotItem
}

// MenuSnapshot is a whole menu as the merchant wants it to be. Publication
// replaces the menu with the snapshot: positions present in it are created or
// updated, positions absent from it are withdrawn. Partial publication does
// not exist, because a merchant integration that lost one item during export
// must not silently keep selling it.
type MenuSnapshot struct {
	MerchantID uuid.UUID
	Categories []SnapshotCategory
}

// FlatItem is one snapshot item together with the category it belongs to.
type FlatItem struct {
	CategoryName string
	Item         SnapshotItem
}

// Normalize trims the free-text fields and orders the snapshot
// deterministically: categories by position and name, items by position and
// external id. The order matters because rows are written in it, so two
// concurrent publications of the same merchant touch rows in the same order.
func (s MenuSnapshot) Normalize() MenuSnapshot {
	out := MenuSnapshot{MerchantID: s.MerchantID}
	out.Categories = make([]SnapshotCategory, 0, len(s.Categories))
	for _, c := range s.Categories {
		nc := SnapshotCategory{
			Name:     strings.TrimSpace(c.Name),
			Position: c.Position,
			Items:    make([]SnapshotItem, 0, len(c.Items)),
		}
		for _, item := range c.Items {
			nc.Items = append(nc.Items, SnapshotItem{
				ExternalID:  strings.TrimSpace(item.ExternalID),
				Name:        strings.TrimSpace(item.Name),
				Description: strings.TrimSpace(item.Description),
				PriceMinor:  item.PriceMinor,
				Position:    item.Position,
			})
		}
		sort.SliceStable(nc.Items, func(i, j int) bool {
			if nc.Items[i].Position != nc.Items[j].Position {
				return nc.Items[i].Position < nc.Items[j].Position
			}
			return nc.Items[i].ExternalID < nc.Items[j].ExternalID
		})
		out.Categories = append(out.Categories, nc)
	}
	sort.SliceStable(out.Categories, func(i, j int) bool {
		if out.Categories[i].Position != out.Categories[j].Position {
			return out.Categories[i].Position < out.Categories[j].Position
		}
		return out.Categories[i].Name < out.Categories[j].Name
	})
	return out
}

// ItemCount returns the number of items in the snapshot.
func (s MenuSnapshot) ItemCount() int {
	n := 0
	for _, c := range s.Categories {
		n += len(c.Items)
	}
	return n
}

// FlatItems returns every item of the snapshot ordered by external id. The
// repository writes rows in this order so that concurrent publications cannot
// deadlock against each other.
func (s MenuSnapshot) FlatItems() []FlatItem {
	out := make([]FlatItem, 0, s.ItemCount())
	for _, c := range s.Categories {
		for _, item := range c.Items {
			out = append(out, FlatItem{CategoryName: c.Name, Item: item})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Item.ExternalID < out[j].Item.ExternalID
	})
	return out
}

// ExternalIDs returns the sorted external identifiers of the snapshot. The
// repository withdraws exactly the published items that are not in this set.
func (s MenuSnapshot) ExternalIDs() []string {
	ids := make([]string, 0, s.ItemCount())
	for _, c := range s.Categories {
		for _, item := range c.Items {
			ids = append(ids, item.ExternalID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Validate checks the snapshot against the business bounds. It expects the
// snapshot to be already normalized and reports every violation at once.
func (s MenuSnapshot) Validate() error {
	ve := &ValidationError{}
	if s.MerchantID == uuid.Nil {
		ve.Add("merchant_id", "is required")
	}
	switch n := len(s.Categories); {
	case n == 0:
		ve.Add("categories", "must contain at least one category")
	case n > MaxCategoriesPerMenu:
		ve.Add("categories", "must not contain more than 50 categories")
	}
	if n := s.ItemCount(); n > MaxItemsPerMenu {
		ve.Add("categories.items", "must not contain more than 500 items in total")
	} else if n == 0 && len(s.Categories) > 0 {
		ve.Add("categories.items", "must contain at least one item")
	}

	seenCategory := make(map[string]struct{}, len(s.Categories))
	seenExternal := make(map[string]struct{}, s.ItemCount())
	for _, c := range s.Categories {
		switch n := len([]rune(c.Name)); {
		case n == 0:
			ve.Add("categories.name", "is required")
		case n > MaxCategoryNameLength:
			ve.Add("categories.name", "must not exceed 100 characters")
		}
		if _, dup := seenCategory[c.Name]; dup {
			ve.Add("categories.name", "must be unique within the menu: "+c.Name)
		}
		seenCategory[c.Name] = struct{}{}
		if c.Position < 0 {
			ve.Add("categories.position", "must not be negative")
		}
		// An empty category cannot be represented in the published menu,
		// because a category is read through the items that reference it.
		if len(c.Items) == 0 {
			ve.Add("categories.items", "category "+c.Name+" must contain at least one item")
		}
		for _, item := range c.Items {
			validateSnapshotItem(ve, item, seenExternal)
		}
	}
	return ve.OrNil()
}

func validateSnapshotItem(ve *ValidationError, item SnapshotItem, seen map[string]struct{}) {
	switch n := len(item.ExternalID); {
	case n == 0:
		ve.Add("items.external_id", "is required")
	case n > MaxExternalIDLength:
		ve.Add("items.external_id", "must not exceed 64 characters")
	default:
		if _, dup := seen[item.ExternalID]; dup {
			ve.Add("items.external_id", "must be unique within the menu: "+item.ExternalID)
		}
		seen[item.ExternalID] = struct{}{}
	}
	validateName(ve, "items.name", item.Name)
	if len([]rune(item.Description)) > MaxItemDescriptionLength {
		ve.Add("items.description", "must not exceed 1000 characters")
	}
	validatePrice(ve, "items.price_minor", item.PriceMinor)
	if item.Position < 0 {
		ve.Add("items.position", "must not be negative")
	}
}
