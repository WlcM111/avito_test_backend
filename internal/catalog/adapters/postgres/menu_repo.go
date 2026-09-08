// Package postgres implements the storage port of catalog-service on top of
// PostgreSQL. It owns the SQL and translates storage failures into domain
// errors; it contains no business rules.
//
// Publication is the only write path with more than one statement, and it runs
// in a single transaction guarded by a row lock on merchant_menu_state. The
// lock is what serialises two publications of the same merchant: without it
// both could read the same version, and the second would silently overwrite
// the first.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const itemColumns = `
	id, merchant_id, category_id, external_id, name, description,
	price_minor, is_deleted, position, updated_at`

// MenuRepository reads and writes menus.
type MenuRepository struct {
	tx *pgxx.TxManager
}

// NewMenuRepository returns a repository bound to the transaction manager.
func NewMenuRepository(tx *pgxx.TxManager) *MenuRepository {
	return &MenuRepository{tx: tx}
}

// readOnlySnapshot bounds the read paths that need more than one statement.
// REPEATABLE READ gives both statements the same snapshot, so a publication
// running in parallel cannot make the returned version and the returned items
// come from different states of the menu.
func readOnlySnapshot() pgx.TxOptions {
	return pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
}

// GetMenu returns the published menu of a merchant.
func (r *MenuRepository) GetMenu(ctx context.Context, merchantID uuid.UUID) (domain.Menu, error) {
	var menu domain.Menu
	err := r.tx.InTx(ctx, readOnlySnapshot(), func(ctx context.Context) error {
		version, updatedAt, err := r.menuState(ctx, merchantID)
		if err != nil {
			return err
		}
		categories, err := r.readCategories(ctx, merchantID)
		if err != nil {
			return err
		}
		menu = domain.Menu{
			MerchantID:  merchantID,
			MenuVersion: version,
			Categories:  categories,
			UpdatedAt:   updatedAt,
		}
		return nil
	})
	if err != nil {
		return domain.Menu{}, err
	}
	return menu, nil
}

// menuState reads the current menu version of a merchant.
func (r *MenuRepository) menuState(ctx context.Context, merchantID uuid.UUID) (int64, time.Time, error) {
	const q = `SELECT menu_version, updated_at FROM merchant_menu_state WHERE merchant_id = $1`
	var (
		version   int64
		updatedAt time.Time
	)
	err := r.tx.Querier(ctx).QueryRow(ctx, q, merchantID).Scan(&version, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, domain.ErrMenuNotFound
	}
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("read menu state: %w", err)
	}
	return version, updatedAt, nil
}

// readCategories returns the categories of a merchant together with their
// published items, in display order. One statement serves the whole menu, so
// the read cost does not grow with the number of categories.
func (r *MenuRepository) readCategories(ctx context.Context, merchantID uuid.UUID) ([]domain.MenuCategory, error) {
	const q = `
SELECT c.id, c.name, c.position,
       i.id, i.merchant_id, i.category_id, i.external_id, i.name, i.description,
       i.price_minor, i.is_deleted, i.position, i.updated_at
FROM menu_categories c
JOIN menu_items i ON i.category_id = c.id AND NOT i.is_deleted
WHERE c.merchant_id = $1
ORDER BY c.position, c.name, c.id, i.position, i.name, i.id`

	rows, err := r.tx.Querier(ctx).Query(ctx, q, merchantID)
	if err != nil {
		return nil, fmt.Errorf("read menu: %w", err)
	}
	defer rows.Close()

	var (
		out     []domain.MenuCategory
		current *domain.MenuCategory
	)
	for rows.Next() {
		var (
			category domain.Category
			item     domain.MenuItem
		)
		category.MerchantID = merchantID
		if err := rows.Scan(&category.ID, &category.Name, &category.Position,
			&item.ID, &item.MerchantID, &item.CategoryID, &item.ExternalID, &item.Name,
			&item.Description, &item.PriceMinor, &item.IsDeleted, &item.Position,
			&item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan menu row: %w", err)
		}
		if current == nil || current.Category.ID != category.ID {
			out = append(out, domain.MenuCategory{Category: category})
			current = &out[len(out)-1]
		}
		current.Items = append(current.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate menu rows: %w", err)
	}
	return out, nil
}

// PublishSnapshot replaces the menu of a merchant with the snapshot.
//
// The whole publication is one transaction:
//  1. the version row is locked, which serialises publications of this
//     merchant and freezes the value the If-Match precondition is checked
//     against;
//  2. the published items are read to tell created from updated and to find
//     the ones the snapshot no longer contains;
//  3. categories and items are upserted in a deterministic order, so two
//     publications of different merchants never take row locks in opposite
//     orders;
//  4. items absent from the snapshot are withdrawn - never deleted, because
//     orders and stock cards reference them;
//  5. the version is incremented last, so a reader either sees the whole old
//     menu or the whole new one.
func (r *MenuRepository) PublishSnapshot(ctx context.Context, snapshot domain.MenuSnapshot,
	expectedVersion *int64) (app.PublishResult, error) {
	var result app.PublishResult
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		current, exists, err := r.lockMenuState(ctx, snapshot.MerchantID)
		if err != nil {
			return err
		}
		if expectedVersion != nil && *expectedVersion != current {
			return domain.ErrVersionConflict
		}

		published, err := r.publishedExternalIDs(ctx, snapshot.MerchantID)
		if err != nil {
			return err
		}

		categoryIDs, err := r.upsertCategories(ctx, snapshot)
		if err != nil {
			return err
		}
		created := 0
		for _, flat := range snapshot.FlatItems() {
			categoryID, ok := categoryIDs[flat.CategoryName]
			if !ok {
				return fmt.Errorf("category %q was not upserted", flat.CategoryName)
			}
			if err := r.upsertItem(ctx, snapshot.MerchantID, categoryID, flat.Item); err != nil {
				return err
			}
			if _, wasPublished := published[flat.Item.ExternalID]; !wasPublished {
				created++
			}
		}

		withdrawn, err := r.withdrawAbsent(ctx, snapshot.MerchantID, snapshot.ExternalIDs())
		if err != nil {
			return err
		}

		version, err := r.bumpVersion(ctx, snapshot.MerchantID, exists)
		if err != nil {
			return err
		}
		categories, err := r.readCategories(ctx, snapshot.MerchantID)
		if err != nil {
			return err
		}
		result = app.PublishResult{
			Menu: domain.Menu{
				MerchantID:  snapshot.MerchantID,
				MenuVersion: version,
				Categories:  categories,
				UpdatedAt:   time.Now().UTC(),
			},
			Created:   created,
			Updated:   snapshot.ItemCount() - created,
			Withdrawn: withdrawn,
		}
		return nil
	})
	if err != nil {
		return app.PublishResult{}, err
	}
	return result, nil
}

// lockMenuState locks and reads the version row. A merchant that has never
// published has no row to lock; the concurrent first publication is caught by
// the primary key of merchant_menu_state instead.
func (r *MenuRepository) lockMenuState(ctx context.Context, merchantID uuid.UUID) (int64, bool, error) {
	const q = `SELECT menu_version FROM merchant_menu_state WHERE merchant_id = $1 FOR UPDATE`
	var version int64
	err := r.tx.Querier(ctx).QueryRow(ctx, q, merchantID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lock menu state: %w", err)
	}
	return version, true, nil
}

func (r *MenuRepository) publishedExternalIDs(ctx context.Context, merchantID uuid.UUID) (map[string]struct{}, error) {
	const q = `SELECT external_id FROM menu_items WHERE merchant_id = $1 AND NOT is_deleted`
	rows, err := r.tx.Querier(ctx).Query(ctx, q, merchantID)
	if err != nil {
		return nil, fmt.Errorf("read published items: %w", err)
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var externalID string
		if err := rows.Scan(&externalID); err != nil {
			return nil, fmt.Errorf("scan published item: %w", err)
		}
		out[externalID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published items: %w", err)
	}
	return out, nil
}

// upsertCategories creates or updates the categories of the snapshot and
// returns their identifiers by name. Categories are written in name order so
// that the lock order is the same in every transaction.
func (r *MenuRepository) upsertCategories(ctx context.Context,
	snapshot domain.MenuSnapshot) (map[string]uuid.UUID, error) {
	names := make([]string, 0, len(snapshot.Categories))
	positions := make(map[string]int32, len(snapshot.Categories))
	for _, c := range snapshot.Categories {
		names = append(names, c.Name)
		positions[c.Name] = c.Position
	}
	sort.Strings(names)

	const q = `
INSERT INTO menu_categories (id, merchant_id, name, position)
VALUES ($1, $2, $3, $4)
ON CONFLICT (merchant_id, name) DO UPDATE SET position = EXCLUDED.position
RETURNING id`

	out := make(map[string]uuid.UUID, len(names))
	for _, name := range names {
		var id uuid.UUID
		if err := r.tx.Querier(ctx).QueryRow(ctx, q,
			uuid.New(), snapshot.MerchantID, name, positions[name]).Scan(&id); err != nil {
			return nil, fmt.Errorf("upsert category %q: %w", name, err)
		}
		out[name] = id
	}
	return out, nil
}

// upsertItem creates or updates one item. The conflict target is
// (merchant_id, external_id), so an item that already exists keeps its
// platform identifier: stock cards and order snapshots point at it.
func (r *MenuRepository) upsertItem(ctx context.Context, merchantID, categoryID uuid.UUID,
	item domain.SnapshotItem) error {
	const q = `
INSERT INTO menu_items (id, merchant_id, category_id, external_id, name,
                        description, price_minor, is_deleted, position, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8, now())
ON CONFLICT (merchant_id, external_id) DO UPDATE
SET category_id = EXCLUDED.category_id,
    name        = EXCLUDED.name,
    description = EXCLUDED.description,
    price_minor = EXCLUDED.price_minor,
    is_deleted  = false,
    position    = EXCLUDED.position,
    updated_at  = now()`

	if _, err := r.tx.Querier(ctx).Exec(ctx, q, uuid.New(), merchantID, categoryID,
		item.ExternalID, item.Name, item.Description, item.PriceMinor, item.Position); err != nil {
		return translateWrite(err, item.ExternalID)
	}
	return nil
}

// withdrawAbsent marks the published items that the snapshot no longer
// contains. Rows are kept because orders and stock cards reference them.
func (r *MenuRepository) withdrawAbsent(ctx context.Context, merchantID uuid.UUID,
	keep []string) (int, error) {
	const q = `
UPDATE menu_items SET is_deleted = true, updated_at = now()
WHERE merchant_id = $1 AND NOT is_deleted AND external_id <> ALL($2::text[])`
	tag, err := r.tx.Querier(ctx).Exec(ctx, q, merchantID, keep)
	if err != nil {
		return 0, fmt.Errorf("withdraw absent items: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// bumpVersion increments the menu version, creating the row on the first
// publication. A unique violation there means another first publication of the
// same merchant committed first, which is a version conflict, not a bug.
func (r *MenuRepository) bumpVersion(ctx context.Context, merchantID uuid.UUID, exists bool) (int64, error) {
	var (
		version int64
		err     error
	)
	if exists {
		const q = `
UPDATE merchant_menu_state SET menu_version = menu_version + 1, updated_at = now()
WHERE merchant_id = $1
RETURNING menu_version`
		err = r.tx.Querier(ctx).QueryRow(ctx, q, merchantID).Scan(&version)
	} else {
		const q = `
INSERT INTO merchant_menu_state (merchant_id, menu_version) VALUES ($1, 1)
RETURNING menu_version`
		err = r.tx.Querier(ctx).QueryRow(ctx, q, merchantID).Scan(&version)
	}
	if err != nil {
		if pgxx.IsUniqueViolation(err) {
			return 0, domain.ErrVersionConflict
		}
		return 0, fmt.Errorf("bump menu version: %w", err)
	}
	return version, nil
}

// UpdateItem changes the name or the price of one published item and bumps the
// menu version in the same transaction: a price is menu content, and a client
// that quoted version N must be able to see that it is no longer current.
func (r *MenuRepository) UpdateItem(ctx context.Context, merchantID, itemID uuid.UUID,
	patch domain.ItemPatch, expectedVersion *int64) (domain.MenuItem, int64, error) {
	var (
		item    domain.MenuItem
		version int64
	)
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		current, exists, err := r.lockMenuState(ctx, merchantID)
		if err != nil {
			return err
		}
		if !exists {
			// Without a published menu there is no published item to change.
			return domain.ErrMenuItemNotFound
		}
		if expectedVersion != nil && *expectedVersion != current {
			return domain.ErrVersionConflict
		}

		const q = `
UPDATE menu_items
SET name        = coalesce($3::text, name),
    price_minor = coalesce($4::bigint, price_minor),
    updated_at  = now()
WHERE id = $1 AND merchant_id = $2 AND NOT is_deleted
RETURNING ` + itemColumns

		updated, err := scanItemRow(r.tx.Querier(ctx).QueryRow(ctx, q,
			itemID, merchantID, patch.Name, patch.PriceMinor))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrMenuItemNotFound
		}
		if err != nil {
			return translateWrite(err, itemID.String())
		}

		version, err = r.bumpVersion(ctx, merchantID, true)
		if err != nil {
			return err
		}
		item = updated
		return nil
	})
	if err != nil {
		return domain.MenuItem{}, 0, err
	}
	return item, version, nil
}

// QuoteItems resolves menu item ids to their current names and prices.
//
// The lookup is deliberately not filtered by merchant: an item that belongs to
// a different merchant has to be told apart from an item that does not exist,
// because the two mean different things to the customer placing the order.
func (r *MenuRepository) QuoteItems(ctx context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) (int64, []domain.Quote, error) {
	var (
		version int64
		quotes  []domain.Quote
	)
	err := r.tx.InTx(ctx, readOnlySnapshot(), func(ctx context.Context) error {
		v, _, err := r.menuState(ctx, merchantID)
		if err != nil && !errors.Is(err, domain.ErrMenuNotFound) {
			return err
		}
		version = v

		const q = `SELECT ` + itemColumns + ` FROM menu_items WHERE id = ANY($1)`
		rows, err := r.tx.Querier(ctx).Query(ctx, q, menuItemIDs)
		if err != nil {
			return fmt.Errorf("quote items: %w", err)
		}
		defer rows.Close()

		found := make(map[uuid.UUID]domain.MenuItem, len(menuItemIDs))
		for rows.Next() {
			item, scanErr := scanItemRow(rows)
			if scanErr != nil {
				return fmt.Errorf("scan quoted item: %w", scanErr)
			}
			found[item.ID] = item
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate quoted items: %w", err)
		}

		quotes = make([]domain.Quote, 0, len(menuItemIDs))
		for _, id := range menuItemIDs {
			quotes = append(quotes, quoteFor(merchantID, id, found))
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return version, quotes, nil
}

// quoteFor classifies one requested identifier.
func quoteFor(merchantID, id uuid.UUID, found map[uuid.UUID]domain.MenuItem) domain.Quote {
	item, ok := found[id]
	switch {
	case !ok:
		return domain.Quote{MenuItemID: id, Problem: domain.ItemProblemNotFound}
	case item.MerchantID != merchantID:
		return domain.Quote{MenuItemID: id, Problem: domain.ItemProblemNotInMerchant}
	case item.IsDeleted:
		return domain.Quote{
			MenuItemID: id,
			ExternalID: item.ExternalID,
			Name:       item.Name,
			Problem:    domain.ItemProblemUnpublished,
		}
	default:
		return domain.Quote{
			MenuItemID:     item.ID,
			ExternalID:     item.ExternalID,
			Name:           item.Name,
			UnitPriceMinor: item.PriceMinor,
			Problem:        domain.ItemProblemNone,
		}
	}
}

// translateWrite converts constraint violations into domain errors so that a
// schema-level guarantee surfaces as a meaningful failure rather than a 500.
func translateWrite(err error, subject string) error {
	if pgxx.IsCheckViolation(err) {
		ve := &domain.ValidationError{}
		ve.Add("items", "rejected by the schema constraint "+
			pgxx.ConstraintName(err)+" for "+subject)
		return ve
	}
	return fmt.Errorf("write menu item %s: %w", subject, err)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItemRow(row rowScanner) (domain.MenuItem, error) {
	var i domain.MenuItem
	err := row.Scan(&i.ID, &i.MerchantID, &i.CategoryID, &i.ExternalID, &i.Name,
		&i.Description, &i.PriceMinor, &i.IsDeleted, &i.Position, &i.UpdatedAt)
	return i, err
}
