// Package testsupport provides in-memory implementations of the catalogue
// storage and directory ports. They exist so that the use-case and transport
// layers can be tested without a database or a running merchant-service. They
// are used by tests only; the SQL behaviour and the gRPC integration
// themselves are covered by the integration and contract tests.
package testsupport

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/google/uuid"
)

// Fixed identifiers matching migrations/catalog/0002_seed.sql, which in turn
// reuses the demonstration menu item ids of migrations/inventory/0002_seed.sql.
var (
	// MerchantID is the demonstration merchant "Кафе «Пример»".
	MerchantID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	// OtherMerchantID is a second merchant, used to prove that an item of one
	// merchant cannot be quoted for another.
	OtherMerchantID = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	// ItemSellable is the item inventory keeps units for.
	ItemSellable = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	// ItemEnabledEmpty is the item inventory keeps an empty card for.
	ItemEnabledEmpty = uuid.MustParse("a1111111-1111-4111-8111-222222222222")
	// ItemStopped is the item inventory keeps on the stop list.
	ItemStopped = uuid.MustParse("a1111111-1111-4111-8111-333333333333")
)

// External identifiers of the seeded items in the merchant's own system.
const (
	ExternalSellable     = "POS-001"
	ExternalEnabledEmpty = "POS-002"
	ExternalStopped      = "POS-003"
)

// DevKey is the development access key of the demonstration merchant.
const DevKey = "dev-merchant-key-kafe-primer-0001"

// MenuRepo is an in-memory, domain-faithful MenuRepository.
type MenuRepo struct {
	mu       sync.Mutex
	items    map[uuid.UUID]domain.MenuItem
	category map[uuid.UUID]domain.Category
	versions map[uuid.UUID]int64
	updated  map[uuid.UUID]time.Time
}

// NewMenuRepo seeds the repository with the demonstration menu.
func NewMenuRepo() *MenuRepo {
	r := &MenuRepo{
		items:    map[uuid.UUID]domain.MenuItem{},
		category: map[uuid.UUID]domain.Category{},
		versions: map[uuid.UUID]int64{},
		updated:  map[uuid.UUID]time.Time{},
	}
	hot := r.putCategory(MerchantID, "Горячее", 0)
	drinks := r.putCategory(MerchantID, "Напитки", 1)
	r.putItem(domain.MenuItem{
		ID: ItemSellable, MerchantID: MerchantID, CategoryID: hot,
		ExternalID: ExternalSellable, Name: "Борщ с говядиной",
		Description: "Классический борщ со сметаной и пампушкой.",
		PriceMinor:  42000, Position: 0,
	})
	r.putItem(domain.MenuItem{
		ID: ItemEnabledEmpty, MerchantID: MerchantID, CategoryID: hot,
		ExternalID: ExternalEnabledEmpty, Name: "Котлета по-киевски",
		Description: "Подаётся с картофельным пюре.",
		PriceMinor:  56000, Position: 1,
	})
	r.putItem(domain.MenuItem{
		ID: ItemStopped, MerchantID: MerchantID, CategoryID: drinks,
		ExternalID: ExternalStopped, Name: "Морс клюквенный",
		Description: "Домашний морс, 400 мл.",
		PriceMinor:  15000, Position: 0,
	})
	r.versions[MerchantID] = 1
	r.updated[MerchantID] = time.Now().UTC()
	return r
}

// AddForeignItem registers an item owned by another merchant, so that a test
// can tell "unknown item" apart from "item of a different merchant".
func (r *MenuRepo) AddForeignItem(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	categoryID := r.putCategory(OtherMerchantID, "Роллы", 0)
	r.putItem(domain.MenuItem{
		ID: id, MerchantID: OtherMerchantID, CategoryID: categoryID,
		ExternalID: "OTHER-001", Name: "Филадельфия", PriceMinor: 99000,
	})
	r.versions[OtherMerchantID] = 1
}

// Version returns the current menu version of a merchant, for assertions.
func (r *MenuRepo) Version(merchantID uuid.UUID) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.versions[merchantID]
}

// Item returns one stored item, for assertions.
func (r *MenuRepo) Item(id uuid.UUID) (domain.MenuItem, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	return item, ok
}

func (r *MenuRepo) putCategory(merchantID uuid.UUID, name string, position int32) uuid.UUID {
	for id, c := range r.category {
		if c.MerchantID == merchantID && c.Name == name {
			c.Position = position
			r.category[id] = c
			return id
		}
	}
	id := uuid.New()
	r.category[id] = domain.Category{
		ID: id, MerchantID: merchantID, Name: name, Position: position,
	}
	return id
}

func (r *MenuRepo) putItem(item domain.MenuItem) {
	item.UpdatedAt = time.Now().UTC()
	r.items[item.ID] = item
}

func (r *MenuRepo) findByExternal(merchantID uuid.UUID, externalID string) (domain.MenuItem, bool) {
	for _, item := range r.items {
		if item.MerchantID == merchantID && item.ExternalID == externalID {
			return item, true
		}
	}
	return domain.MenuItem{}, false
}

// GetMenu returns the published menu of a merchant.
func (r *MenuRepo) GetMenu(_ context.Context, merchantID uuid.UUID) (domain.Menu, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.menuLocked(merchantID)
}

func (r *MenuRepo) menuLocked(merchantID uuid.UUID) (domain.Menu, error) {
	version, ok := r.versions[merchantID]
	if !ok {
		return domain.Menu{}, domain.ErrMenuNotFound
	}
	byCategory := map[uuid.UUID][]domain.MenuItem{}
	for _, item := range r.items {
		if item.MerchantID != merchantID || item.IsDeleted {
			continue
		}
		byCategory[item.CategoryID] = append(byCategory[item.CategoryID], item)
	}
	categories := make([]domain.MenuCategory, 0, len(byCategory))
	for categoryID, items := range byCategory {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Position != items[j].Position {
				return items[i].Position < items[j].Position
			}
			return items[i].Name < items[j].Name
		})
		categories = append(categories, domain.MenuCategory{
			Category: r.category[categoryID], Items: items,
		})
	}
	sort.Slice(categories, func(i, j int) bool {
		if categories[i].Category.Position != categories[j].Category.Position {
			return categories[i].Category.Position < categories[j].Category.Position
		}
		return categories[i].Category.Name < categories[j].Category.Name
	})
	return domain.Menu{
		MerchantID:  merchantID,
		MenuVersion: version,
		Categories:  categories,
		UpdatedAt:   r.updated[merchantID],
	}, nil
}

// PublishSnapshot replaces the menu of a merchant with the snapshot.
func (r *MenuRepo) PublishSnapshot(_ context.Context, snapshot domain.MenuSnapshot,
	expectedVersion *int64) (app.PublishResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.versions[snapshot.MerchantID]
	if expectedVersion != nil && *expectedVersion != current {
		return app.PublishResult{}, domain.ErrVersionConflict
	}

	published := map[string]struct{}{}
	for _, item := range r.items {
		if item.MerchantID == snapshot.MerchantID && !item.IsDeleted {
			published[item.ExternalID] = struct{}{}
		}
	}

	keep := map[string]struct{}{}
	created := 0
	for _, c := range snapshot.Categories {
		categoryID := r.putCategory(snapshot.MerchantID, c.Name, c.Position)
		for _, si := range c.Items {
			keep[si.ExternalID] = struct{}{}
			if _, was := published[si.ExternalID]; !was {
				created++
			}
			existing, found := r.findByExternal(snapshot.MerchantID, si.ExternalID)
			id := existing.ID
			if !found {
				id = uuid.New()
			}
			r.putItem(domain.MenuItem{
				ID: id, MerchantID: snapshot.MerchantID, CategoryID: categoryID,
				ExternalID: si.ExternalID, Name: si.Name, Description: si.Description,
				PriceMinor: si.PriceMinor, IsDeleted: false, Position: si.Position,
			})
		}
	}

	withdrawn := 0
	for id, item := range r.items {
		if item.MerchantID != snapshot.MerchantID || item.IsDeleted {
			continue
		}
		if _, stays := keep[item.ExternalID]; stays {
			continue
		}
		item.IsDeleted = true
		item.UpdatedAt = time.Now().UTC()
		r.items[id] = item
		withdrawn++
	}

	r.versions[snapshot.MerchantID] = current + 1
	r.updated[snapshot.MerchantID] = time.Now().UTC()
	menu, err := r.menuLocked(snapshot.MerchantID)
	if err != nil {
		return app.PublishResult{}, err
	}
	return app.PublishResult{
		Menu:      menu,
		Created:   created,
		Updated:   snapshot.ItemCount() - created,
		Withdrawn: withdrawn,
	}, nil
}

// UpdateItem changes one published item and bumps the menu version.
func (r *MenuRepo) UpdateItem(_ context.Context, merchantID, itemID uuid.UUID,
	patch domain.ItemPatch, expectedVersion *int64) (domain.MenuItem, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, published := r.versions[merchantID]
	if !published {
		return domain.MenuItem{}, 0, domain.ErrMenuItemNotFound
	}
	if expectedVersion != nil && *expectedVersion != current {
		return domain.MenuItem{}, 0, domain.ErrVersionConflict
	}
	item, ok := r.items[itemID]
	if !ok || item.MerchantID != merchantID || item.IsDeleted {
		return domain.MenuItem{}, 0, domain.ErrMenuItemNotFound
	}
	if patch.Name != nil {
		item.Name = *patch.Name
	}
	if patch.PriceMinor != nil {
		item.PriceMinor = *patch.PriceMinor
	}
	r.putItem(item)
	r.versions[merchantID] = current + 1
	r.updated[merchantID] = time.Now().UTC()
	return r.items[itemID], r.versions[merchantID], nil
}

// QuoteItems resolves menu item ids to their current names and prices.
func (r *MenuRepo) QuoteItems(_ context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) (int64, []domain.Quote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	quotes := make([]domain.Quote, 0, len(menuItemIDs))
	for _, id := range menuItemIDs {
		item, ok := r.items[id]
		switch {
		case !ok:
			quotes = append(quotes, domain.Quote{
				MenuItemID: id, Problem: domain.ItemProblemNotFound,
			})
		case item.MerchantID != merchantID:
			quotes = append(quotes, domain.Quote{
				MenuItemID: id, Problem: domain.ItemProblemNotInMerchant,
			})
		case item.IsDeleted:
			quotes = append(quotes, domain.Quote{
				MenuItemID: id, ExternalID: item.ExternalID, Name: item.Name,
				Problem: domain.ItemProblemUnpublished,
			})
		default:
			quotes = append(quotes, domain.Quote{
				MenuItemID: item.ID, ExternalID: item.ExternalID, Name: item.Name,
				UnitPriceMinor: item.PriceMinor, Problem: domain.ItemProblemNone,
			})
		}
	}
	return r.versions[merchantID], quotes, nil
}

// MerchantDirectory is an in-memory MerchantDirectory.
type MerchantDirectory struct {
	Keys map[string]app.MerchantIdentity
	Err  error
}

// NewMerchantDirectory maps the development key to the demonstration merchant.
func NewMerchantDirectory() *MerchantDirectory {
	return &MerchantDirectory{Keys: map[string]app.MerchantIdentity{
		DevKey: {MerchantID: MerchantID, Active: true},
	}}
}

// Authenticate resolves an access key.
func (d *MerchantDirectory) Authenticate(_ context.Context, apiKey string) (app.MerchantIdentity, error) {
	if d.Err != nil {
		return app.MerchantIdentity{}, d.Err
	}
	identity, ok := d.Keys[apiKey]
	if !ok {
		return app.MerchantIdentity{}, app.ErrMerchantUnauthenticated
	}
	return identity, nil
}
