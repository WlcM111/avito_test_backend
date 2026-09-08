package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReservationRepository holds, commits and releases units.
type ReservationRepository struct {
	tx    *pgxx.TxManager
	stock *StockRepository
}

// NewReservationRepository returns a repository bound to the transaction
// manager. It reuses the stock repository to append journal entries so that
// the journal format lives in exactly one place.
func NewReservationRepository(tx *pgxx.TxManager, stock *StockRepository) *ReservationRepository {
	return &ReservationRepository{tx: tx, stock: stock}
}

// Reserve holds the requested units for every line atomically.
//
// The hold is taken in one transaction:
//  1. the reservation row is claimed with ON CONFLICT DO NOTHING, which makes
//     a repeat of the same reservation id hold nothing again (INV-S6);
//  2. every line is applied with a conditional UPDATE that both checks the
//     stop list and the available units and performs the hold (INV-S4);
//  3. lines are applied in ascending menu item order, so two concurrent
//     multi-item reservations lock rows in the same order and cannot deadlock;
//  4. the first line that cannot be held aborts the whole transaction, so a
//     partial hold is impossible.
func (r *ReservationRepository) Reserve(ctx context.Context, req domain.ReserveRequest) (app.ReserveOutcome, error) {
	var out app.ReserveOutcome
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		expiresAt := time.Now().UTC().Add(req.TTL)

		const claim = `
INSERT INTO stock_reservations (id, merchant_id, state, expires_at)
VALUES ($1, $2, 'HELD', $3)
ON CONFLICT (id) DO NOTHING
RETURNING id`
		var claimed uuid.UUID
		claimErr := r.tx.Querier(ctx).QueryRow(ctx, claim,
			req.ReservationID, req.MerchantID, expiresAt).Scan(&claimed)
		if errors.Is(claimErr, pgx.ErrNoRows) {
			existing, err := r.get(ctx, req.ReservationID)
			if err != nil {
				return err
			}
			out = app.ReserveOutcome{Reservation: existing, AlreadyExisted: true}
			return nil
		}
		if claimErr != nil {
			return fmt.Errorf("claim reservation: %w", claimErr)
		}

		reservation := domain.Reservation{
			ID:         req.ReservationID,
			MerchantID: req.MerchantID,
			State:      domain.ReservationHeld,
			ExpiresAt:  expiresAt,
		}

		const hold = `
UPDATE stock_items
SET reserved_qty = reserved_qty + $3, version = version + 1, updated_at = now()
WHERE merchant_id = $1 AND menu_item_id = $2
  AND sales_state = 'ENABLED'
  AND on_hand_qty - reserved_qty >= $3
RETURNING ` + stockColumns

		for _, line := range req.Lines {
			item, err := scanStockRow(r.tx.Querier(ctx).QueryRow(ctx, hold,
				req.MerchantID, line.MenuItemID, line.Qty))
			if errors.Is(err, pgx.ErrNoRows) {
				// Nothing was held for this line. Build the full picture for
				// every requested line, then abort: the caller needs to see
				// all problems at once, not only the first one.
				results, diagErr := r.diagnose(ctx, req)
				if diagErr != nil {
					return diagErr
				}
				return &domain.ReservationRejectedError{Lines: results}
			}
			if err != nil {
				return translateStockWrite(err)
			}

			const insertLine = `
INSERT INTO stock_reservation_lines (reservation_id, stock_item_id, menu_item_id, qty)
VALUES ($1, $2, $3, $4)`
			if _, err := r.tx.Querier(ctx).Exec(ctx, insertLine,
				req.ReservationID, item.ID, line.MenuItemID, line.Qty); err != nil {
				return fmt.Errorf("insert reservation line: %w", err)
			}

			if err := r.stock.recordMovement(ctx, movementRecord{
				StockItemID:   item.ID,
				Kind:          domain.MovementReserve,
				DeltaOnHand:   0,
				DeltaReserved: line.Qty,
				OnHandAfter:   item.OnHandQty,
				ReservedAfter: item.ReservedQty,
				ReservationID: &req.ReservationID,
			}); err != nil {
				return err
			}

			reservation.Lines = append(reservation.Lines, domain.ReservationLine{
				StockItemID: item.ID,
				MenuItemID:  line.MenuItemID,
				Qty:         line.Qty,
			})
		}

		out = app.ReserveOutcome{Reservation: reservation}
		return nil
	})
	if err != nil {
		return app.ReserveOutcome{}, err
	}
	return out, nil
}

// diagnose reports the state of every requested line. It runs inside the
// failing transaction, so the numbers it returns are the ones the conditional
// UPDATE saw.
func (r *ReservationRepository) diagnose(ctx context.Context, req domain.ReserveRequest) ([]domain.LineResult, error) {
	ids := make([]uuid.UUID, 0, len(req.Lines))
	for _, l := range req.Lines {
		ids = append(ids, l.MenuItemID)
	}
	items, err := r.stock.GetMany(ctx, req.MerchantID, ids)
	if err != nil {
		return nil, err
	}
	byItem := make(map[uuid.UUID]domain.StockItem, len(items))
	for _, item := range items {
		byItem[item.MenuItemID] = item
	}

	results := make([]domain.LineResult, 0, len(req.Lines))
	for _, l := range req.Lines {
		item, tracked := byItem[l.MenuItemID]
		if !tracked {
			results = append(results, domain.LineResult{
				MenuItemID:   l.MenuItemID,
				RequestedQty: l.Qty,
				Problem:      domain.LineProblemNotTracked,
			})
			continue
		}
		results = append(results, domain.LineResult{
			MenuItemID:   l.MenuItemID,
			RequestedQty: l.Qty,
			AvailableQty: item.AvailableQty(),
			Problem:      item.CanReserve(l.Qty),
		})
	}
	return results, nil
}

// Commit consumes the held units.
func (r *ReservationRepository) Commit(ctx context.Context, reservationID uuid.UUID) (app.TransitionOutcome, error) {
	return r.finish(ctx, reservationID, domain.ReservationCommitted, domain.MovementCommit, "")
}

// Release returns the held units to availability.
func (r *ReservationRepository) Release(ctx context.Context, reservationID uuid.UUID,
	reason domain.ReleaseReason) (app.TransitionOutcome, error) {
	kind := domain.MovementRelease
	if reason == domain.ReleaseTTLExpired {
		kind = domain.MovementExpire
	}
	return r.finish(ctx, reservationID, domain.ReservationReleased, kind, string(reason))
}

// finish performs the single terminal transition of a hold. The UPDATE is
// conditional on the state still being HELD, which is what makes the
// transition happen exactly once even when a commit, a release and the sweeper
// race each other (INV-S5).
func (r *ReservationRepository) finish(ctx context.Context, reservationID uuid.UUID,
	target domain.ReservationState, kind domain.MovementKind, reason string) (app.TransitionOutcome, error) {
	var out app.TransitionOutcome
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		const q = `
UPDATE stock_reservations
SET state = $2, updated_at = now()
WHERE id = $1 AND state = 'HELD'
RETURNING id`
		var claimed uuid.UUID
		err := r.tx.Querier(ctx).QueryRow(ctx, q, reservationID, string(target)).Scan(&claimed)
		if errors.Is(err, pgx.ErrNoRows) {
			current, getErr := r.get(ctx, reservationID)
			if getErr != nil {
				return getErr
			}
			if current.State == target {
				out = app.TransitionOutcome{State: current.State, AlreadyApplied: true}
				return nil
			}
			return fmt.Errorf("%w: current state is %s", domain.ErrReservationNotHeld, current.State)
		}
		if err != nil {
			return fmt.Errorf("finish reservation: %w", err)
		}

		lines, err := r.lines(ctx, reservationID)
		if err != nil {
			return err
		}
		for _, line := range lines {
			var (
				stmt        string
				deltaOnHand int32
			)
			if target == domain.ReservationCommitted {
				// Consuming units removes them from both counters at once.
				stmt = `
UPDATE stock_items
SET on_hand_qty = on_hand_qty - $2, reserved_qty = reserved_qty - $2,
    version = version + 1, updated_at = now()
WHERE id = $1
RETURNING ` + stockColumns
				deltaOnHand = -line.Qty
			} else {
				stmt = `
UPDATE stock_items
SET reserved_qty = reserved_qty - $2, version = version + 1, updated_at = now()
WHERE id = $1
RETURNING ` + stockColumns
			}
			item, err := scanStockRow(r.tx.Querier(ctx).QueryRow(ctx, stmt, line.StockItemID, line.Qty))
			if err != nil {
				return translateStockWrite(err)
			}
			if err := r.stock.recordMovement(ctx, movementRecord{
				StockItemID:   item.ID,
				Kind:          kind,
				DeltaOnHand:   deltaOnHand,
				DeltaReserved: -line.Qty,
				OnHandAfter:   item.OnHandQty,
				ReservedAfter: item.ReservedQty,
				ReservationID: &reservationID,
				Reason:        reason,
			}); err != nil {
				return err
			}
		}
		out = app.TransitionOutcome{State: target}
		return nil
	})
	if err != nil {
		return app.TransitionOutcome{}, err
	}
	return out, nil
}

// Get returns one hold with its lines.
func (r *ReservationRepository) Get(ctx context.Context, reservationID uuid.UUID) (domain.Reservation, error) {
	return r.get(ctx, reservationID)
}

func (r *ReservationRepository) get(ctx context.Context, reservationID uuid.UUID) (domain.Reservation, error) {
	const q = `
SELECT id, merchant_id, state, expires_at, created_at, updated_at
FROM stock_reservations WHERE id = $1`
	var res domain.Reservation
	err := r.tx.Querier(ctx).QueryRow(ctx, q, reservationID).Scan(
		&res.ID, &res.MerchantID, &res.State, &res.ExpiresAt, &res.CreatedAt, &res.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Reservation{}, domain.ErrReservationNotFound
	}
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("get reservation: %w", err)
	}
	lines, err := r.lines(ctx, reservationID)
	if err != nil {
		return domain.Reservation{}, err
	}
	res.Lines = lines
	return res, nil
}

func (r *ReservationRepository) lines(ctx context.Context, reservationID uuid.UUID) ([]domain.ReservationLine, error) {
	const q = `
SELECT stock_item_id, menu_item_id, qty
FROM stock_reservation_lines WHERE reservation_id = $1
ORDER BY menu_item_id`
	rows, err := r.tx.Querier(ctx).Query(ctx, q, reservationID)
	if err != nil {
		return nil, fmt.Errorf("list reservation lines: %w", err)
	}
	defer rows.Close()

	var lines []domain.ReservationLine
	for rows.Next() {
		var l domain.ReservationLine
		if err := rows.Scan(&l.StockItemID, &l.MenuItemID, &l.Qty); err != nil {
			return nil, fmt.Errorf("scan reservation line: %w", err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reservation lines: %w", err)
	}
	return lines, nil
}

// ReleaseExpired releases holds whose TTL has passed. The batch is picked with
// FOR UPDATE SKIP LOCKED so that several sweeper instances divide the work
// instead of blocking each other or releasing the same hold twice.
func (r *ReservationRepository) ReleaseExpired(ctx context.Context, now time.Time, batch int) (int, error) {
	var ids []uuid.UUID
	err := r.tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		const q = `
SELECT id FROM stock_reservations
WHERE state = 'HELD' AND expires_at < $1
ORDER BY expires_at
LIMIT $2
FOR UPDATE SKIP LOCKED`
		rows, err := r.tx.Querier(ctx).Query(ctx, q, now, batch)
		if err != nil {
			return fmt.Errorf("select expired reservations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan expired reservation: %w", err)
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}

	released := 0
	for _, id := range ids {
		outcome, err := r.Release(ctx, id, domain.ReleaseTTLExpired)
		switch {
		case err == nil && !outcome.AlreadyApplied:
			released++
		case err == nil:
			// Another actor finished the hold between the scan and the release.
		case errors.Is(err, domain.ErrReservationNotHeld),
			errors.Is(err, domain.ErrReservationNotFound):
			// Lost the race against a commit or release; nothing to do.
		default:
			return released, fmt.Errorf("release expired reservation %s: %w", id, err)
		}
	}
	return released, nil
}
