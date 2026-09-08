package domain_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/google/uuid"
)

var merchantID = uuid.MustParse("11111111-1111-4111-8111-111111111111")

func snapshot(categories ...domain.SnapshotCategory) domain.MenuSnapshot {
	return domain.MenuSnapshot{MerchantID: merchantID, Categories: categories}
}

func item(externalID, name string, price int64, position int32) domain.SnapshotItem {
	return domain.SnapshotItem{
		ExternalID: externalID, Name: name, PriceMinor: price, Position: position,
	}
}

func TestSnapshotValidateAcceptsAWellFormedMenu(t *testing.T) {
	t.Parallel()
	s := snapshot(domain.SnapshotCategory{
		Name:  "Горячее",
		Items: []domain.SnapshotItem{item("POS-001", "Борщ", 42000, 0)},
	}).Normalize()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestSnapshotValidateRejectsDuplicateExternalID(t *testing.T) {
	t.Parallel()
	s := snapshot(
		domain.SnapshotCategory{
			Name:  "Горячее",
			Items: []domain.SnapshotItem{item("POS-001", "Борщ", 42000, 0)},
		},
		domain.SnapshotCategory{
			Name:  "Напитки",
			Items: []domain.SnapshotItem{item("POS-001", "Морс", 15000, 0)},
		},
	).Normalize()

	err := s.Validate()
	ve, ok := domain.AsValidationError(err)
	if !ok {
		t.Fatalf("Validate() = %v, want a validation error", err)
	}
	if !hasField(ve, "items.external_id") {
		t.Fatalf("violations %+v do not mention the duplicate external id", ve.Violations)
	}
}

func TestSnapshotValidateRejectsDuplicateCategoryName(t *testing.T) {
	t.Parallel()
	s := snapshot(
		domain.SnapshotCategory{
			Name:  "Горячее",
			Items: []domain.SnapshotItem{item("POS-001", "Борщ", 42000, 0)},
		},
		domain.SnapshotCategory{
			Name:  "Горячее",
			Items: []domain.SnapshotItem{item("POS-002", "Котлета", 56000, 0)},
		},
	).Normalize()

	ve, ok := domain.AsValidationError(s.Validate())
	if !ok || !hasField(ve, "categories.name") {
		t.Fatalf("a duplicate category name must be rejected, got %+v", ve)
	}
}

// The schema stores a category through the items that reference it, so a
// category without items cannot be represented in a published menu.
func TestSnapshotValidateRejectsEmptyCategory(t *testing.T) {
	t.Parallel()
	s := snapshot(domain.SnapshotCategory{Name: "Горячее"}).Normalize()
	ve, ok := domain.AsValidationError(s.Validate())
	if !ok || !hasField(ve, "categories.items") {
		t.Fatalf("an empty category must be rejected, got %+v", ve)
	}
}

func TestSnapshotValidateRejectsBadItems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		item  domain.SnapshotItem
		field string
	}{
		{name: "zero price", item: item("POS-001", "Борщ", 0, 0), field: "items.price_minor"},
		{name: "negative price", item: item("POS-001", "Борщ", -100, 0), field: "items.price_minor"},
		{
			name:  "price above the cap",
			item:  item("POS-001", "Борщ", domain.MaxPriceMinor+1, 0),
			field: "items.price_minor",
		},
		{name: "missing external id", item: item("", "Борщ", 42000, 0), field: "items.external_id"},
		{
			name:  "external id too long",
			item:  item(strings.Repeat("x", domain.MaxExternalIDLength+1), "Борщ", 42000, 0),
			field: "items.external_id",
		},
		{name: "missing name", item: item("POS-001", "", 42000, 0), field: "items.name"},
		{
			name:  "negative position",
			item:  item("POS-001", "Борщ", 42000, -1),
			field: "items.position",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := snapshot(domain.SnapshotCategory{
				Name: "Горячее", Items: []domain.SnapshotItem{tc.item},
			}).Normalize()
			ve, ok := domain.AsValidationError(s.Validate())
			if !ok || !hasField(ve, tc.field) {
				t.Fatalf("violations %+v do not mention %q", ve, tc.field)
			}
		})
	}
}

func TestSnapshotValidateRejectsAnEmptyMenu(t *testing.T) {
	t.Parallel()
	ve, ok := domain.AsValidationError(snapshot().Normalize().Validate())
	if !ok || !hasField(ve, "categories") {
		t.Fatalf("an empty menu must be rejected, got %+v", ve)
	}
}

func TestSnapshotValidateRejectsAMissingMerchant(t *testing.T) {
	t.Parallel()
	s := domain.MenuSnapshot{Categories: []domain.SnapshotCategory{{
		Name: "Горячее", Items: []domain.SnapshotItem{item("POS-001", "Борщ", 42000, 0)},
	}}}
	ve, ok := domain.AsValidationError(s.Normalize().Validate())
	if !ok || !hasField(ve, "merchant_id") {
		t.Fatalf("a snapshot without a merchant must be rejected, got %+v", ve)
	}
}

func TestNormalizeTrimsAndOrders(t *testing.T) {
	t.Parallel()
	s := snapshot(
		domain.SnapshotCategory{
			Name:     "  Напитки  ",
			Position: 1,
			Items: []domain.SnapshotItem{
				item(" POS-004 ", "  Чай  ", 9000, 1),
				item("POS-003", "Морс", 15000, 0),
			},
		},
		domain.SnapshotCategory{
			Name:  "Горячее",
			Items: []domain.SnapshotItem{item("POS-001", "Борщ", 42000, 0)},
		},
	).Normalize()

	if s.Categories[0].Name != "Горячее" || s.Categories[1].Name != "Напитки" {
		t.Fatalf("categories are not ordered by position: %+v", s.Categories)
	}
	drinks := s.Categories[1]
	if drinks.Items[0].ExternalID != "POS-003" || drinks.Items[1].ExternalID != "POS-004" {
		t.Fatalf("items are not ordered by position: %+v", drinks.Items)
	}
	if drinks.Items[1].Name != "Чай" {
		t.Fatalf("name was not trimmed: %q", drinks.Items[1].Name)
	}
}

// Rows are written in the order FlatItems returns, so the order must not
// depend on how the merchant grouped the items.
func TestFlatItemsAreOrderedByExternalID(t *testing.T) {
	t.Parallel()
	s := snapshot(
		domain.SnapshotCategory{
			Name: "Напитки", Position: 1,
			Items: []domain.SnapshotItem{item("POS-003", "Морс", 15000, 0)},
		},
		domain.SnapshotCategory{
			Name: "Горячее",
			Items: []domain.SnapshotItem{
				item("POS-002", "Котлета", 56000, 1),
				item("POS-001", "Борщ", 42000, 0),
			},
		},
	).Normalize()

	flat := s.FlatItems()
	if len(flat) != 3 {
		t.Fatalf("got %d flat items, want 3", len(flat))
	}
	for i, want := range []string{"POS-001", "POS-002", "POS-003"} {
		if flat[i].Item.ExternalID != want {
			t.Fatalf("flat[%d] = %q, want %q", i, flat[i].Item.ExternalID, want)
		}
	}
	if flat[2].CategoryName != "Напитки" {
		t.Fatalf("flat item lost its category: %+v", flat[2])
	}
}

func TestExternalIDsAndItemCount(t *testing.T) {
	t.Parallel()
	s := snapshot(domain.SnapshotCategory{
		Name: "Горячее",
		Items: []domain.SnapshotItem{
			item("POS-002", "Котлета", 56000, 1),
			item("POS-001", "Борщ", 42000, 0),
		},
	}).Normalize()

	if s.ItemCount() != 2 {
		t.Fatalf("ItemCount() = %d, want 2", s.ItemCount())
	}
	ids := s.ExternalIDs()
	if len(ids) != 2 || ids[0] != "POS-001" || ids[1] != "POS-002" {
		t.Fatalf("ExternalIDs() = %v", ids)
	}
}

func TestSnapshotValidateRejectsTooManyCategories(t *testing.T) {
	t.Parallel()
	categories := make([]domain.SnapshotCategory, 0, domain.MaxCategoriesPerMenu+1)
	for i := 0; i <= domain.MaxCategoriesPerMenu; i++ {
		name := "cat-" + strconv.Itoa(i)
		categories = append(categories, domain.SnapshotCategory{
			Name:  name,
			Items: []domain.SnapshotItem{item("POS-"+name, "Позиция", 1000, 0)},
		})
	}
	ve, ok := domain.AsValidationError(snapshot(categories...).Normalize().Validate())
	if !ok || !hasField(ve, "categories") {
		t.Fatalf("an oversized menu must be rejected, got %+v", ve)
	}
}
