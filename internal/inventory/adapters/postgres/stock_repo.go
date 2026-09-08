// Package postgres implements the storage ports of inventory-service on top of
// PostgreSQL. It owns the SQL and translates storage failures into domain
// errors; it contains no business rules.
//
// Every quantity change is expressed as a single conditional UPDATE so that
// PostgreSQL locks the row and re-checks the condition itself. That is what
// makes the accounting correct under concurrency without an explicit lock and
// without an isolation level above READ COMMITTED.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const stockColumns = `
	id, merchant_id, menu_item_id, sales_state, on_hand_qty, reserved_qty,
	version, created_at, updated_at`

// StockRepository reads and writes stock cards and their journal.
type StockRepository struct {
	tx *pgxx.TxManager
}

// NewStockRepository returns a repository bound to the transaction manager.
func NewStockRepository(tx *pgxx.TxManager) *StockRepository {
	return &StockRepository{tx: tx}
}

// Get returns one card of the merchant.
func (r *StockRepository) Get(ctx context.Context, merchantID, menuItemID uuid.UUID) (domain.StockItem, error) {
	const q = `SELECT ` + stockColumns + `
FROM stock_items WHERE merchant_id = $1 AND menu_item_id = $2`
	item, err := scanStockRow(r.tx.Querier(ctx).QueryRow(ctx, q, merchantID, menuItemID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StockItem{}, domain.ErrStockItemNotFound
	}
	if err != nil {
		return domain.StockItem{}, fmt.Errorf("get stock item: %w", err)
	}
	return item, nil
}

// List returns one page of the merchant's cards with the total count taken in
// the same snapshot.
func (r *StockRepository) List(ctx context.Context, merchantID uuid.UUID,
	limit, offset int) ([]domain.StockItem, int, error) {
	const q = `SELECT ` + stockColumns + `, count(*) OVER () AS total_count
FROM stock_items WHERE merchant_id = $1
ORDER BY menu_item_id
LIMIT $2 OFFSET $3`

	rows, err := r.tx.Querier(ctx).Query(ctx, q, merchantID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list stock items: %w", err)
	}
	defer rows.Close()

	var (
		items []domain.StockItem
		total int
	)
	for rows.Next() {
		var (
			item  domain.StockItem
			count int
		)
		if err := scanStockRows(rows, &item, &count); err != nil {
			return nil, 0, fmt.Errorf("scan stock item: %w", err)
		}
		total = count
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate stock items: %w", err)
	}
	return items, total, nil
}

// GetMany returns the cards that exist among menuItemIDs.
func (r *StockRepository) GetMany(ctx context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) ([]domain.StockItem, error) {
	if len(menuItemIDs) == 0 {
		return nil, nil
	}
	const q = `SELECT ` + stockColumns + `
FROM stock_items WHERE merchant_id = $1 AND menu_item_id = ANY($2)`

	rows, err := r.tx.Querier(ctx).Query(ctx, q, merchantID, menuItemIDs)
	if err != nil {
		return nil, fmt.Errorf("get stock items: %w", err)
	}
	defer rows.Close()

	var items []domain.StockItem
	for rows.Next() {
		var item domain.StockItem
		if err := scanStockRows(rows, &item, nil); err != nil {
			return nil, fmt.Errorf("scan stock item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stock items: %w", err)
	}
	return items, nil
}

// ensureCard creates the card if it is absent and returns its current state.
// A new card starts at zero units and ENABLED: creating it confirms nothing.
func (r *StockRepository) ensureCard(ctx context.Context, merchantID, menuItemID uuid.UUID) (domain.StockItem, error) {
	const q = `
INSERT INTO stock_items (id, merchant_id, menu_item_id, sales_state, on_hand_qty, reserved_qty)
VALUES ($1, $2, $3, 'ENABLED', 0, 0)
ON CONFLICT (merchant_id, menu_item_id) DO NOTHING`
	if _, err := r.tx.Querier(ctx).Exec(ctx, q, uuid.New(), merchantID, menuItemID); err != nil {
		return domain.StockItem{}, fmt.Errorf("create stock item: %w", err)
	}
	return r.Get(ctx, merchantID, menuItemID)
}

// SetOnHand stores an absolute on-hand value in one transaction together with
// its journal entry (INV-S9). The UPDATE refuses to drop below the held units,
// so the caller learns about the conflict instead of losing reservations.
func (r *StockRepository) SetOnHand(ctx context.Context, merchantID, menuItemID uuid.UUID,
	qty int32, expectedVersion *int64, reason string) (domain.StockItem, error) {
	var out domain.StockItem
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		current, err := r.ensureCard(ctx, merchantID, menuItemID)
		if err != nil {
			return err
		}
		if expectedVersion != nil && *expectedVersion != current.Version {
			return domain.ErrVersionConflict
		}
		if qty < current.ReservedQty {
			return domain.ErrBelowReserved
		}

		const q = `
UPDATE stock_items
SET on_hand_qty = $3, version = version + 1, updated_at = now()
WHERE merchant_id = $1 AND menu_item_id = $2
  AND ($4::bigint IS NULL OR version = $4)
  AND $3 >= reserved_qty
RETURNING ` + stockColumns

		updated, err := scanStockRow(r.tx.Querier(ctx).QueryRow(ctx, q,
			merchantID, menuItemID, qty, expectedVersion))
		if errors.Is(err, pgx.ErrNoRows) {
			// The row changed between the read and the write: re-read to tell
			// a stale version apart from a concurrent reservation.
			latest, getErr := r.Get(ctx, merchantID, menuItemID)
			if getErr != nil {
				return getErr
			}
			if expectedVersion != nil && *expectedVersion != latest.Version {
				return domain.ErrVersionConflict
			}
			return domain.ErrBelowReserved
		}
		if err != nil {
			return translateStockWrite(err)
		}

		delta := updated.OnHandQty - current.OnHandQty
		if err := r.recordMovement(ctx, movementRecord{
			StockItemID:   updated.ID,
			Kind:          domain.MovementSet,
			DeltaOnHand:   delta,
			DeltaReserved: 0,
			OnHandAfter:   updated.OnHandQty,
			ReservedAfter: updated.ReservedQty,
			Reason:        reason,
		}); err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return domain.StockItem{}, err
	}
	return out, nil
}

type adjustResult struct {
	OnHandQty int32 `json:"on_hand_qty"`
	Version   int64 `json:"version"`
	Delta     int32 `json:"delta"`
}

// AdjustOnHand applies a relative change exactly once per operation key.
// A relative change is not idempotent by nature, so the key is inserted in the
// same transaction as the change: a repeat hits the primary key of
// stock_operations, the transaction is abandoned and the stored result is
// returned unchanged (INV-S7).
func (r *StockRepository) AdjustOnHand(ctx context.Context, merchantID, menuItemID uuid.UUID,
	delta int32, operationKey, reason string) (domain.StockItem, bool, error) {
	var (
		out       domain.StockItem
		duplicate bool
	)
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		current, err := r.ensureCard(ctx, merchantID, menuItemID)
		if err != nil {
			return err
		}

		// Claim the operation key first; losing the race means a repeat.
		const claim = `
INSERT INTO stock_operations (stock_item_id, operation_key, result)
VALUES ($1, $2, $3)
ON CONFLICT (stock_item_id, operation_key) DO NOTHING
RETURNING operation_key`
		placeholder, _ := json.Marshal(adjustResult{})
		var claimed string
		claimErr := r.tx.Querier(ctx).QueryRow(ctx, claim,
			current.ID, operationKey, placeholder).Scan(&claimed)
		if errors.Is(claimErr, pgx.ErrNoRows) {
			duplicate = true
			out, err = r.Get(ctx, merchantID, menuItemID)
			return err
		}
		if claimErr != nil {
			return fmt.Errorf("claim operation key: %w", claimErr)
		}

		next, err := current.ApplyAdjustment(delta)
		if err != nil {
			return err
		}

		const q = `
UPDATE stock_items
SET on_hand_qty = $3, version = version + 1, updated_at = now()
WHERE merchant_id = $1 AND menu_item_id = $2 AND $3 >= reserved_qty
RETURNING ` + stockColumns

		updated, err := scanStockRow(r.tx.Querier(ctx).QueryRow(ctx, q, merchantID, menuItemID, next))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrBelowReserved
		}
		if err != nil {
			return translateStockWrite(err)
		}

		stored, _ := json.Marshal(adjustResult{
			OnHandQty: updated.OnHandQty, Version: updated.Version, Delta: delta,
		})
		if _, err := r.tx.Querier(ctx).Exec(ctx,
			`UPDATE stock_operations SET result = $3
			 WHERE stock_item_id = $1 AND operation_key = $2`,
			current.ID, operationKey, stored); err != nil {
			return fmt.Errorf("store operation result: %w", err)
		}

		if err := r.recordMovement(ctx, movementRecord{
			StockItemID:   updated.ID,
			Kind:          domain.MovementAdjust,
			DeltaOnHand:   delta,
			DeltaReserved: 0,
			OnHandAfter:   updated.OnHandQty,
			ReservedAfter: updated.ReservedQty,
			OperationKey:  operationKey,
			Reason:        reason,
		}); err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return domain.StockItem{}, false, err
	}
	return out, duplicate, nil
}

// SetSalesState changes only the stop-list switch. The statement deliberately
// touches no quantity column: enabling sales must never confirm units
// (INV-S8). No journal entry is written because no quantity changed.
func (r *StockRepository) SetSalesState(ctx context.Context, merchantID, menuItemID uuid.UUID,
	state domain.SalesState, expectedVersion *int64) (domain.StockItem, error) {
	var out domain.StockItem
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		current, err := r.ensureCard(ctx, merchantID, menuItemID)
		if err != nil {
			return err
		}
		if expectedVersion != nil && *expectedVersion != current.Version {
			return domain.ErrVersionConflict
		}

		const q = `
UPDATE stock_items
SET sales_state = $3, version = version + 1, updated_at = now()
WHERE merchant_id = $1 AND menu_item_id = $2
  AND ($4::bigint IS NULL OR version = $4)
RETURNING ` + stockColumns

		updated, err := scanStockRow(r.tx.Querier(ctx).QueryRow(ctx, q,
			merchantID, menuItemID, string(state), expectedVersion))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVersionConflict
		}
		if err != nil {
			return translateStockWrite(err)
		}
		out = updated
		return nil
	})
	if err != nil {
		return domain.StockItem{}, err
	}
	return out, nil
}

// ListMovements returns one page of the journal of a card, newest first.
func (r *StockRepository) ListMovements(ctx context.Context, merchantID, menuItemID uuid.UUID,
	limit, offset int) ([]domain.Movement, int, error) {
	item, err := r.Get(ctx, merchantID, menuItemID)
	if err != nil {
		return nil, 0, err
	}
	const q = `
SELECT id, stock_item_id, kind, delta_on_hand, delta_reserved,
       on_hand_after, reserved_after, reservation_id, coalesce(operation_key, ''),
       reason, created_at, count(*) OVER () AS total_count
FROM stock_movements
WHERE stock_item_id = $1
ORDER BY id DESC
LIMIT $2 OFFSET $3`

	rows, err := r.tx.Querier(ctx).Query(ctx, q, item.ID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list movements: %w", err)
	}
	defer rows.Close()

	var (
		items []domain.Movement
		total int
	)
	for rows.Next() {
		var (
			m     domain.Movement
			count int
		)
		if err := rows.Scan(&m.ID, &m.StockItemID, &m.Kind, &m.DeltaOnHand, &m.DeltaReserved,
			&m.OnHandAfter, &m.ReservedAfter, &m.ReservationID, &m.OperationKey,
			&m.Reason, &m.CreatedAt, &count); err != nil {
			return nil, 0, fmt.Errorf("scan movement: %w", err)
		}
		m.MenuItemID = item.MenuItemID
		total = count
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate movements: %w", err)
	}
	return items, total, nil
}

type movementRecord struct {
	StockItemID   uuid.UUID
	Kind          domain.MovementKind
	DeltaOnHand   int32
	DeltaReserved int32
	OnHandAfter   int32
	ReservedAfter int32
	ReservationID *uuid.UUID
	OperationKey  string
	Reason        string
}

// recordMovement appends one journal entry. It is always called inside the
// transaction that changed the quantities, which is what makes INV-S9 hold.
func (r *StockRepository) recordMovement(ctx context.Context, m movementRecord) error {
	const q = `
INSERT INTO stock_movements (stock_item_id, kind, delta_on_hand, delta_reserved,
                             on_hand_after, reserved_after, reservation_id,
                             operation_key, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	var key *string
	if m.OperationKey != "" {
		key = &m.OperationKey
	}
	if _, err := r.tx.Querier(ctx).Exec(ctx, q, m.StockItemID, string(m.Kind),
		m.DeltaOnHand, m.DeltaReserved, m.OnHandAfter, m.ReservedAfter,
		m.ReservationID, key, m.Reason); err != nil {
		return fmt.Errorf("record movement: %w", err)
	}
	return nil
}

// translateStockWrite converts constraint violations into domain errors so
// that a schema-level guarantee surfaces as a meaningful failure.
func translateStockWrite(err error) error {
	if pgxx.IsCheckViolation(err) {
		switch pgxx.ConstraintName(err) {
		case "stock_items_reserved_le_on_hand_chk":
			return domain.ErrBelowReserved
		case "stock_items_on_hand_chk":
			return domain.ErrNegativeResult
		}
	}
	return fmt.Errorf("write stock item: %w", err)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanStockRow(row rowScanner) (domain.StockItem, error) {
	var s domain.StockItem
	err := row.Scan(&s.ID, &s.MerchantID, &s.MenuItemID, &s.SalesState,
		&s.OnHandQty, &s.ReservedQty, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func scanStockRows(rows pgx.Rows, s *domain.StockItem, count *int) error {
	dest := []any{&s.ID, &s.MerchantID, &s.MenuItemID, &s.SalesState,
		&s.OnHandQty, &s.ReservedQty, &s.Version, &s.CreatedAt, &s.UpdatedAt}
	if count != nil {
		dest = append(dest, count)
	}
	return rows.Scan(dest...)
}
