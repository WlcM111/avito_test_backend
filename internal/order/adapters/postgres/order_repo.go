// Package postgres implements the storage ports of order-service on top of
// PostgreSQL. It owns the SQL and translates storage failures into domain
// errors; it contains no business rules.
//
// Two transactions carry the correctness of this service. Placement writes the
// order and its positions together, so an order can never exist without the
// prices the customer agreed to. A lifecycle move writes the new status and
// the outbox message together, so a decision can never be recorded without its
// delivery being scheduled - that is the whole point of the outbox.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const orderColumns = `
	id, merchant_id, merchant_name, customer_id, idempotency_key,
	customer_name, customer_phone, delivery_address, comment, status,
	items_total_minor, delivery_fee_minor, total_minor, currency,
	eta_minutes, reject_reason, created_at, updated_at`

// OrderRepository reads and writes orders, their positions and the outbox
// messages their lifecycle produces.
type OrderRepository struct {
	tx *pgxx.TxManager
}

// NewOrderRepository returns a repository bound to the transaction manager.
func NewOrderRepository(tx *pgxx.TxManager) *OrderRepository {
	return &OrderRepository{tx: tx}
}

// Create inserts the order and its positions in one transaction.
//
// The insert is conditional on the idempotency key: a repeat does not create a
// second order, it reports the stored one. The UNIQUE constraint is what makes
// this safe under concurrency - two simultaneous submits of the same key take
// the same index entry, and exactly one of them wins.
func (r *OrderRepository) Create(ctx context.Context, order domain.Order) (domain.Order, bool, error) {
	var (
		stored    domain.Order
		duplicate bool
	)
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		const insert = `
INSERT INTO orders (
    id, merchant_id, merchant_name, customer_id, idempotency_key,
    customer_name, customer_phone, delivery_address, comment, status,
    items_total_minor, delivery_fee_minor, total_minor, currency,
    eta_minutes, reject_reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, '')
ON CONFLICT (customer_id, idempotency_key) DO NOTHING
RETURNING ` + orderColumns

		created, err := scanOrderRow(r.tx.Querier(ctx).QueryRow(ctx, insert,
			order.ID, order.MerchantID, order.MerchantName, order.CustomerID,
			order.IdempotencyKey, order.CustomerName, order.CustomerPhone,
			order.DeliveryAddress, order.Comment, string(order.Status),
			order.ItemsTotalMinor, order.DeliveryFeeMinor, order.TotalMinor,
			order.Currency, order.ETAMinutes))
		if errors.Is(err, pgx.ErrNoRows) {
			duplicate = true
			existing, findErr := r.findByKey(ctx, order.CustomerID, order.IdempotencyKey)
			if findErr != nil {
				return findErr
			}
			stored = existing
			return nil
		}
		if err != nil {
			return translateOrderWrite(err)
		}

		if err := r.insertItems(ctx, created.ID, order.Items); err != nil {
			return err
		}
		created.Items = order.Items
		stored = created
		return nil
	})
	if err != nil {
		return domain.Order{}, false, err
	}
	return stored, duplicate, nil
}

// insertItems writes every position in one statement. A round trip per
// position would make placement latency depend on the size of the basket.
func (r *OrderRepository) insertItems(ctx context.Context, orderID uuid.UUID, items []domain.Item) error {
	if len(items) == 0 {
		return fmt.Errorf("order %s has no positions", orderID)
	}
	menuItemIDs := make([]uuid.UUID, 0, len(items))
	externalIDs := make([]string, 0, len(items))
	names := make([]string, 0, len(items))
	prices := make([]int64, 0, len(items))
	quantities := make([]int32, 0, len(items))
	lineTotals := make([]int64, 0, len(items))
	for _, item := range items {
		menuItemIDs = append(menuItemIDs, item.MenuItemID)
		externalIDs = append(externalIDs, item.ExternalID)
		names = append(names, item.Name)
		prices = append(prices, item.UnitPriceMinor)
		quantities = append(quantities, item.Qty)
		lineTotals = append(lineTotals, item.LineTotalMinor)
	}

	const q = `
INSERT INTO order_items (order_id, menu_item_id, external_id, name,
                         unit_price_minor, qty, line_total_minor)
SELECT $1, u.menu_item_id, u.external_id, u.name, u.unit_price_minor, u.qty, u.line_total_minor
FROM unnest($2::uuid[], $3::text[], $4::text[], $5::bigint[], $6::int[], $7::bigint[])
     AS u(menu_item_id, external_id, name, unit_price_minor, qty, line_total_minor)`

	if _, err := r.tx.Querier(ctx).Exec(ctx, q, orderID, menuItemIDs, externalIDs,
		names, prices, quantities, lineTotals); err != nil {
		return translateOrderWrite(err)
	}
	return nil
}

// GetByID returns one order with its positions.
func (r *OrderRepository) GetByID(ctx context.Context, orderID uuid.UUID) (domain.Order, error) {
	const q = `SELECT ` + orderColumns + ` FROM orders WHERE id = $1`
	order, err := scanOrderRow(r.tx.Querier(ctx).QueryRow(ctx, q, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	items, err := r.itemsOf(ctx, []uuid.UUID{orderID})
	if err != nil {
		return domain.Order{}, err
	}
	order.Items = items[orderID]
	return order, nil
}

// FindByIdempotencyKey returns the order a customer created with that key.
func (r *OrderRepository) FindByIdempotencyKey(ctx context.Context, customerID uuid.UUID,
	key string) (domain.Order, error) {
	return r.findByKey(ctx, customerID, key)
}

func (r *OrderRepository) findByKey(ctx context.Context, customerID uuid.UUID,
	key string) (domain.Order, error) {
	const q = `SELECT ` + orderColumns + `
FROM orders WHERE customer_id = $1 AND idempotency_key = $2`
	order, err := scanOrderRow(r.tx.Querier(ctx).QueryRow(ctx, q, customerID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("find order by idempotency key: %w", err)
	}
	items, err := r.itemsOf(ctx, []uuid.UUID{order.ID})
	if err != nil {
		return domain.Order{}, err
	}
	order.Items = items[order.ID]
	return order, nil
}

// ListByCustomer returns one page of the customer's history, newest first.
func (r *OrderRepository) ListByCustomer(ctx context.Context, customerID uuid.UUID,
	limit, offset int) (app.OrderPage, error) {
	const q = `SELECT ` + orderColumns + `, count(*) OVER () AS total_count
FROM orders WHERE customer_id = $1
ORDER BY created_at DESC, id
LIMIT $2 OFFSET $3`
	return r.page(ctx, q, limit, offset, customerID, limit, offset)
}

// ListByMerchant returns one page of the merchant's queue, oldest first, so
// that polling shows the orders in the order they arrived.
func (r *OrderRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID,
	statuses []domain.Status, limit, offset int) (app.OrderPage, error) {
	filter := make([]string, 0, len(statuses))
	for _, st := range statuses {
		filter = append(filter, string(st))
	}
	const q = `SELECT ` + orderColumns + `, count(*) OVER () AS total_count
FROM orders
WHERE merchant_id = $1
  AND (coalesce(array_length($2::text[], 1), 0) = 0 OR status = ANY($2))
ORDER BY created_at, id
LIMIT $3 OFFSET $4`
	return r.page(ctx, q, limit, offset, merchantID, filter, limit, offset)
}

// page runs a paginated query and attaches the positions of the whole page in
// one additional statement, so the cost does not grow with the page size.
func (r *OrderRepository) page(ctx context.Context, q string, limit, offset int,
	args ...any) (app.OrderPage, error) {
	rows, err := r.tx.Querier(ctx).Query(ctx, q, args...)
	if err != nil {
		return app.OrderPage{}, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	var (
		orders []domain.Order
		ids    []uuid.UUID
		total  int
	)
	for rows.Next() {
		var (
			order domain.Order
			count int
		)
		if err := scanOrderInto(rows, &order, &count); err != nil {
			return app.OrderPage{}, fmt.Errorf("scan order: %w", err)
		}
		total = count
		orders = append(orders, order)
		ids = append(ids, order.ID)
	}
	if err := rows.Err(); err != nil {
		return app.OrderPage{}, fmt.Errorf("iterate orders: %w", err)
	}

	items, err := r.itemsOf(ctx, ids)
	if err != nil {
		return app.OrderPage{}, err
	}
	for i := range orders {
		orders[i].Items = items[orders[i].ID]
	}
	return app.OrderPage{Items: orders, Total: total, Limit: limit, Offset: offset}, nil
}

// itemsOf returns the positions of every requested order, grouped by order.
func (r *OrderRepository) itemsOf(ctx context.Context,
	orderIDs []uuid.UUID) (map[uuid.UUID][]domain.Item, error) {
	out := make(map[uuid.UUID][]domain.Item, len(orderIDs))
	if len(orderIDs) == 0 {
		return out, nil
	}
	const q = `
SELECT order_id, menu_item_id, external_id, name, unit_price_minor, qty, line_total_minor
FROM order_items
WHERE order_id = ANY($1)
ORDER BY order_id, name, menu_item_id`

	rows, err := r.tx.Querier(ctx).Query(ctx, q, orderIDs)
	if err != nil {
		return nil, fmt.Errorf("list order items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			orderID uuid.UUID
			item    domain.Item
		)
		if err := rows.Scan(&orderID, &item.MenuItemID, &item.ExternalID, &item.Name,
			&item.UnitPriceMinor, &item.Qty, &item.LineTotalMinor); err != nil {
			return nil, fmt.Errorf("scan order item: %w", err)
		}
		out[orderID] = append(out[orderID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order items: %w", err)
	}
	return out, nil
}

// Transition applies one lifecycle move and, in the same transaction, inserts
// the outbox message the move requires.
//
// The order row is locked first, so the check of the current status and the
// write of the new one cannot be separated by a concurrent decision: without
// the lock a merchant confirming an order and a customer cancelling it could
// both read CREATED and both succeed, releasing units of a confirmed order.
func (r *OrderRepository) Transition(ctx context.Context,
	cmd app.TransitionCommand) (app.TransitionResult, error) {
	var out app.TransitionResult
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		const lock = `SELECT ` + orderColumns + ` FROM orders WHERE id = $1 FOR UPDATE`
		current, err := scanOrderRow(r.tx.Querier(ctx).QueryRow(ctx, lock, cmd.OrderID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrOrderNotFound
		}
		if err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		// An order of another owner is reported as absent, not as forbidden.
		if cmd.ExpectMerchantID != nil && current.MerchantID != *cmd.ExpectMerchantID {
			return domain.ErrOrderNotFound
		}
		if cmd.ExpectCustomerID != nil && current.CustomerID != *cmd.ExpectCustomerID {
			return domain.ErrOrderNotFound
		}

		items, err := r.itemsOf(ctx, []uuid.UUID{current.ID})
		if err != nil {
			return err
		}
		current.Items = items[current.ID]

		// Repeating a move that already happened is a success that changes
		// nothing. It is what makes the merchant operations idempotent without
		// an extra header, and it must not schedule a second message.
		if current.Status == cmd.Target {
			out = app.TransitionResult{Order: current, AlreadyApplied: true}
			return nil
		}
		if !current.Status.CanTransitionTo(cmd.Target) {
			return &domain.StateConflictError{Current: current.Status, Target: cmd.Target}
		}

		const update = `
UPDATE orders
SET status        = $2,
    eta_minutes   = CASE WHEN $3::int > 0 THEN $3 ELSE eta_minutes END,
    reject_reason = CASE WHEN $4::text <> '' THEN $4 ELSE reject_reason END,
    updated_at    = now()
WHERE id = $1 AND status = $5
RETURNING ` + orderColumns

		updated, err := scanOrderRow(r.tx.Querier(ctx).QueryRow(ctx, update,
			cmd.OrderID, string(cmd.Target), cmd.ETAMinutes, cmd.RejectReason,
			string(current.Status)))
		if errors.Is(err, pgx.ErrNoRows) {
			// The row is locked by this transaction, so a lost update is not
			// possible; an empty result would mean the status changed under
			// the lock, which cannot happen.
			return fmt.Errorf("order %s changed while locked", cmd.OrderID)
		}
		if err != nil {
			return translateOrderWrite(err)
		}
		updated.Items = current.Items

		if cmd.Message != nil {
			if err := r.insertMessage(ctx, *cmd.Message); err != nil {
				return err
			}
		}
		out = app.TransitionResult{Order: updated}
		return nil
	})
	if err != nil {
		return app.TransitionResult{}, err
	}
	return out, nil
}

// outboxPayload is the stored shape of a decision. It lives in the adapter
// because the serialisation format is a storage concern, not a business rule.
type outboxPayload struct {
	ReservationID string `json:"reservation_id"`
	Reason        string `json:"reason,omitempty"`
}

// insertMessage writes one outbox message. It is only ever called inside the
// transaction that changed the status, which is what removes the dual write.
func (r *OrderRepository) insertMessage(ctx context.Context, msg domain.Message) error {
	payload, err := json.Marshal(outboxPayload{
		ReservationID: msg.ReservationID.String(),
		Reason:        string(msg.Reason),
	})
	if err != nil {
		return fmt.Errorf("encode outbox payload: %w", err)
	}
	const q = `
INSERT INTO outbox_messages (id, aggregate_id, type, payload, correlation_id, state, available_at)
VALUES ($1, $2, $3, $4, $5, 'PENDING', now())`
	if _, err := r.tx.Querier(ctx).Exec(ctx, q, msg.ID, msg.AggregateID,
		string(msg.Type), payload, msg.CorrelationID); err != nil {
		return fmt.Errorf("insert outbox message: %w", err)
	}
	return nil
}

// translateOrderWrite converts constraint violations into domain errors so
// that a schema-level guarantee surfaces as a meaningful failure.
func translateOrderWrite(err error) error {
	if pgxx.IsUniqueViolation(err) {
		switch pgxx.ConstraintName(err) {
		case "orders_customer_idempotency_key", "orders_pkey":
			return fmt.Errorf("duplicate order: %w", err)
		}
	}
	if pgxx.IsCheckViolation(err) {
		ve := &domain.ValidationError{}
		ve.Add("order", "rejected by the schema constraint "+pgxx.ConstraintName(err))
		return ve
	}
	return fmt.Errorf("write order: %w", err)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOrderRow(row rowScanner) (domain.Order, error) {
	var o domain.Order
	err := scanOrderInto(row, &o, nil)
	return o, err
}

func scanOrderInto(row rowScanner, o *domain.Order, count *int) error {
	dest := []any{
		&o.ID, &o.MerchantID, &o.MerchantName, &o.CustomerID, &o.IdempotencyKey,
		&o.CustomerName, &o.CustomerPhone, &o.DeliveryAddress, &o.Comment, &o.Status,
		&o.ItemsTotalMinor, &o.DeliveryFeeMinor, &o.TotalMinor, &o.Currency,
		&o.ETAMinutes, &o.RejectReason, &o.CreatedAt, &o.UpdatedAt,
	}
	if count != nil {
		dest = append(dest, count)
	}
	return row.Scan(dest...)
}

// ensure the repository keeps satisfying the port it is written for.
var _ app.OrderRepository = (*OrderRepository)(nil)
