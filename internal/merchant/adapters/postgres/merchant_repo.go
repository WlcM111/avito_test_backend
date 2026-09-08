// Package postgres implements the storage ports of merchant-service on top of
// PostgreSQL. It owns the SQL and translates storage failures into domain
// errors; it contains no business rules.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const merchantColumns = `
	id, slug, name, description, cuisine, city, address, phone,
	status, is_accepting_orders, min_order_minor, delivery_fee_minor,
	avg_prep_minutes, version, created_at, updated_at`

// MerchantRepository reads and writes merchants.
type MerchantRepository struct {
	tx *pgxx.TxManager
}

// NewMerchantRepository returns a repository bound to the transaction manager.
func NewMerchantRepository(tx *pgxx.TxManager) *MerchantRepository {
	return &MerchantRepository{tx: tx}
}

// List returns one page of the public merchant list and the total number of
// matches. Only ACTIVE merchants are public. The total is taken with a window
// function so that the page and the count come from a single round trip and a
// single snapshot.
func (r *MerchantRepository) List(ctx context.Context, f domain.ListFilter) ([]domain.Merchant, int, error) {
	const q = `
SELECT ` + merchantColumns + `, count(*) OVER () AS total_count
FROM merchants
WHERE status = 'ACTIVE'
  AND ($1 = '' OR city = $1)
  AND ($2 = '' OR name ILIKE '%' || $2 || '%' OR cuisine ILIKE '%' || $2 || '%')
  AND (NOT $3::boolean OR is_accepting_orders)
ORDER BY name, id
LIMIT $4 OFFSET $5`

	rows, err := r.tx.Querier(ctx).Query(ctx, q, f.City, f.Query, f.OnlyAccepting, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query merchants: %w", err)
	}
	defer rows.Close()

	var (
		items []domain.Merchant
		total int
	)
	for rows.Next() {
		var (
			m     domain.Merchant
			count int
		)
		if err := scanMerchantWithCount(rows, &m, &count); err != nil {
			return nil, 0, fmt.Errorf("scan merchant: %w", err)
		}
		total = count
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate merchants: %w", err)
	}
	return items, total, nil
}

// GetByID returns one merchant regardless of its status.
func (r *MerchantRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Merchant, error) {
	const q = `SELECT ` + merchantColumns + ` FROM merchants WHERE id = $1`
	row := r.tx.Querier(ctx).QueryRow(ctx, q, id)
	m, err := scanMerchantRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Merchant{}, domain.ErrMerchantNotFound
	}
	if err != nil {
		return domain.Merchant{}, fmt.Errorf("get merchant %s: %w", id, err)
	}
	return m, nil
}

// GetByIDs returns the merchants that exist among ids. Unknown ids are omitted.
func (r *MerchantRepository) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Merchant, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	const q = `SELECT ` + merchantColumns + ` FROM merchants WHERE id = ANY($1) ORDER BY name, id`
	rows, err := r.tx.Querier(ctx).Query(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("query merchants by ids: %w", err)
	}
	defer rows.Close()

	var items []domain.Merchant
	for rows.Next() {
		var m domain.Merchant
		if err := scanMerchantWithCount(rows, &m, nil); err != nil {
			return nil, fmt.Errorf("scan merchant: %w", err)
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate merchants: %w", err)
	}
	return items, nil
}

// UpdateProfile applies the patch in a single conditional statement. NULL
// patch fields leave the stored value untouched. When expectedVersion is not
// nil the WHERE clause also requires the version to match, which turns a lost
// update into an explicit domain.ErrVersionConflict.
func (r *MerchantRepository) UpdateProfile(ctx context.Context, id uuid.UUID,
	patch domain.ProfilePatch, expectedVersion *int64) (domain.Merchant, error) {
	const q = `
UPDATE merchants SET
    is_accepting_orders = COALESCE($2, is_accepting_orders),
    min_order_minor     = COALESCE($3, min_order_minor),
    delivery_fee_minor  = COALESCE($4, delivery_fee_minor),
    avg_prep_minutes    = COALESCE($5, avg_prep_minutes),
    version             = version + 1,
    updated_at          = now()
WHERE id = $1 AND ($6::bigint IS NULL OR version = $6)
RETURNING ` + merchantColumns

	row := r.tx.Querier(ctx).QueryRow(ctx, q, id,
		patch.IsAcceptingOrders, patch.MinOrderMinor, patch.DeliveryFeeMinor,
		patch.AvgPrepMinutes, expectedVersion)

	m, err := scanMerchantRow(row)
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if pgxx.IsCheckViolation(err) {
			return domain.Merchant{}, fmt.Errorf("update merchant %s violates %s: %w",
				id, pgxx.ConstraintName(err), err)
		}
		return domain.Merchant{}, fmt.Errorf("update merchant %s: %w", id, err)
	}

	// No row was updated: either the merchant is gone or the version is stale.
	if _, getErr := r.GetByID(ctx, id); getErr != nil {
		return domain.Merchant{}, getErr
	}
	return domain.Merchant{}, domain.ErrVersionConflict
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMerchantRow(row rowScanner) (domain.Merchant, error) {
	var m domain.Merchant
	err := row.Scan(
		&m.ID, &m.Slug, &m.Name, &m.Description, &m.Cuisine, &m.City, &m.Address,
		&m.Phone, &m.Status, &m.IsAcceptingOrders, &m.MinOrderMinor,
		&m.DeliveryFeeMinor, &m.AvgPrepMinutes, &m.Version, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func scanMerchantWithCount(rows pgx.Rows, m *domain.Merchant, count *int) error {
	dest := []any{
		&m.ID, &m.Slug, &m.Name, &m.Description, &m.Cuisine, &m.City, &m.Address,
		&m.Phone, &m.Status, &m.IsAcceptingOrders, &m.MinOrderMinor,
		&m.DeliveryFeeMinor, &m.AvgPrepMinutes, &m.Version, &m.CreatedAt, &m.UpdatedAt,
	}
	if count != nil {
		dest = append(dest, count)
	}
	return rows.Scan(dest...)
}
