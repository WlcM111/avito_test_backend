package domain

import (
	"time"

	"github.com/google/uuid"
)

// Bounds of the catalogue. They are business limits and are enforced in the
// domain as well as by CHECK constraints in the schema.
const (
	// MaxCategoryNameLength caps the name of one menu section.
	MaxCategoryNameLength = 100
	// MaxItemNameLength caps the displayed name of one item.
	MaxItemNameLength = 200
	// MaxItemDescriptionLength caps the free-text description of one item.
	MaxItemDescriptionLength = 1000
	// MaxExternalIDLength caps the identifier of the item in the merchant's
	// own system.
	MaxExternalIDLength = 64
	// MaxPriceMinor caps one price at 1 000 000 RUB expressed in kopecks. It
	// exists so that a misplaced decimal point in a merchant integration is
	// rejected instead of published.
	MaxPriceMinor = 100_000_000
	// MaxCategoriesPerMenu caps one published snapshot.
	MaxCategoriesPerMenu = 50
	// MaxItemsPerMenu caps one published snapshot.
	MaxItemsPerMenu = 500
	// MaxQuoteBatch caps one QuoteItems request.
	MaxQuoteBatch = 100
	// Currency is the only currency of the platform; prices are whole minor
	// units (kopecks) of it.
	Currency = "RUB"
)

// Category is one section of a merchant menu.
type Category struct {
	ID         uuid.UUID
	MerchantID uuid.UUID
	Name       string
	Position   int32
}

// MenuItem is one sellable position of a merchant menu.
//
// ID is the platform identifier: it is what inventory-service keeps a stock
// card for and what an order references. ExternalID is the identifier of the
// same position in the merchant's own system (A-10) and is the key a published
// snapshot is matched by, which is what keeps ID stable across publications.
type MenuItem struct {
	ID          uuid.UUID
	MerchantID  uuid.UUID
	CategoryID  uuid.UUID
	ExternalID  string
	Name        string
	Description string
	PriceMinor  int64
	IsDeleted   bool
	Position    int32
	UpdatedAt   time.Time
}

// MenuCategory is one category of a read menu together with its items.
type MenuCategory struct {
	Category Category
	Items    []MenuItem
}

// Menu is the published menu of one merchant at one version.
type Menu struct {
	MerchantID  uuid.UUID
	MenuVersion int64
	Categories  []MenuCategory
	UpdatedAt   time.Time
}

// ItemProblem explains why one requested item cannot be quoted.
type ItemProblem string

const (
	// ItemProblemNone means the item can be quoted.
	ItemProblemNone ItemProblem = "NONE"
	// ItemProblemNotFound means no item with that id exists in the catalogue.
	ItemProblemNotFound ItemProblem = "NOT_FOUND"
	// ItemProblemNotInMerchant means the item exists but belongs to a
	// different merchant, so it cannot be ordered from this one.
	ItemProblemNotInMerchant ItemProblem = "NOT_IN_MERCHANT"
	// ItemProblemUnpublished means the item belongs to the merchant but was
	// withdrawn from the published menu.
	ItemProblemUnpublished ItemProblem = "UNPUBLISHED"
)

// Quote is the resolved name and price of one requested item. A problem never
// fails the whole request: the caller needs the outcome of every line at once.
type Quote struct {
	MenuItemID     uuid.UUID
	ExternalID     string
	Name           string
	UnitPriceMinor int64
	Problem        ItemProblem
}

// ItemPatch is a partial change of one published item. A nil field leaves the
// stored value untouched, which is how "absent" is told apart from "empty".
type ItemPatch struct {
	Name       *string
	PriceMinor *int64
}

// IsEmpty reports whether the patch would change nothing.
func (p ItemPatch) IsEmpty() bool { return p.Name == nil && p.PriceMinor == nil }

// Validate checks the patch against the business bounds.
func (p ItemPatch) Validate() error {
	ve := &ValidationError{}
	if p.IsEmpty() {
		ve.Add("body", "must change at least one field")
	}
	if p.Name != nil {
		validateName(ve, "name", *p.Name)
	}
	if p.PriceMinor != nil {
		validatePrice(ve, "price_minor", *p.PriceMinor)
	}
	return ve.OrNil()
}

func validateName(ve *ValidationError, field, name string) {
	switch n := len([]rune(name)); {
	case n == 0:
		ve.Add(field, "is required")
	case n > MaxItemNameLength:
		ve.Add(field, "must not exceed 200 characters")
	}
}

func validatePrice(ve *ValidationError, field string, price int64) {
	switch {
	case price <= 0:
		ve.Add(field, "must be greater than zero")
	case price > MaxPriceMinor:
		ve.Add(field, "must not exceed 100000000 minor units")
	}
}
