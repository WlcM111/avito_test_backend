package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/google/uuid"
)

// OutboxRepository reads and updates the transactional outbox.
type OutboxRepository struct {
	tx *pgxx.TxManager
}

// NewOutboxRepository returns a repository bound to the transaction manager.
func NewOutboxRepository(tx *pgxx.TxManager) *OutboxRepository {
	return &OutboxRepository{tx: tx}
}

// Claim takes up to batch due messages for this dispatcher.
//
// FOR UPDATE SKIP LOCKED lets several dispatcher instances divide the work
// instead of blocking each other. The claim also moves the next visibility of
// each message beyond the lease and counts the attempt, so a dispatcher that
// dies mid-flight does not hide the message forever and does not let a second
// instance send it at the same moment. The claim commits before any network
// call is made: keeping a transaction open across gRPC would hold a database
// connection for the duration of someone else's outage.
func (r *OutboxRepository) Claim(ctx context.Context, now time.Time, batch int,
	lease time.Duration) ([]domain.Message, error) {
	if batch <= 0 {
		batch = 1
	}
	if lease <= 0 {
		lease = time.Minute
	}
	const q = `
WITH due AS (
    SELECT id
    FROM outbox_messages
    WHERE state = 'PENDING' AND available_at <= $1
    ORDER BY available_at, id
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE outbox_messages m
SET attempts     = m.attempts + 1,
    available_at = $1 + make_interval(secs => $3),
    updated_at   = now()
FROM due
WHERE m.id = due.id
RETURNING m.id, m.aggregate_id, m.type, m.payload, m.correlation_id, m.state,
          m.attempts, m.available_at, m.last_error, m.created_at, m.updated_at`

	rows, err := r.tx.Querier(ctx).Query(ctx, q, now, batch, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim outbox messages: %w", err)
	}
	defer rows.Close()

	var out []domain.Message
	for rows.Next() {
		var (
			msg     domain.Message
			kind    string
			state   string
			payload []byte
		)
		if err := rows.Scan(&msg.ID, &msg.AggregateID, &kind, &payload,
			&msg.CorrelationID, &state, &msg.Attempts, &msg.AvailableAt,
			&msg.LastError, &msg.CreatedAt, &msg.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan outbox message: %w", err)
		}
		msg.Type = domain.MessageType(kind)
		msg.State = domain.MessageState(state)
		if err := decodePayload(payload, &msg); err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox messages: %w", err)
	}
	return out, nil
}

func decodePayload(raw []byte, msg *domain.Message) error {
	var payload outboxPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode outbox payload of %s: %w", msg.ID, err)
	}
	reservationID, err := uuid.Parse(payload.ReservationID)
	if err != nil {
		return fmt.Errorf("outbox message %s carries an invalid reservation id: %w", msg.ID, err)
	}
	msg.ReservationID = reservationID
	msg.Reason = domain.ReleaseReason(payload.Reason)
	return nil
}

// MarkSent records a delivered message. The update is conditional on the
// message still being pending, so a late acknowledgement cannot revive a
// message a human already parked.
func (r *OutboxRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	const q = `
UPDATE outbox_messages
SET state = 'SENT', last_error = '', updated_at = now()
WHERE id = $1 AND state = 'PENDING'`
	if _, err := r.tx.Querier(ctx).Exec(ctx, q, id); err != nil {
		return fmt.Errorf("mark outbox message sent: %w", err)
	}
	return nil
}

// Reschedule keeps a message pending and moves its next attempt.
func (r *OutboxRepository) Reschedule(ctx context.Context, id uuid.UUID,
	availableAt time.Time, lastErr string) error {
	const q = `
UPDATE outbox_messages
SET available_at = $2, last_error = $3, updated_at = now()
WHERE id = $1 AND state = 'PENDING'`
	if _, err := r.tx.Querier(ctx).Exec(ctx, q, id, availableAt, truncateError(lastErr)); err != nil {
		return fmt.Errorf("reschedule outbox message: %w", err)
	}
	return nil
}

// MarkFailed parks a message that must not be retried.
func (r *OutboxRepository) MarkFailed(ctx context.Context, id uuid.UUID, lastErr string) error {
	const q = `
UPDATE outbox_messages
SET state = 'FAILED', last_error = $2, updated_at = now()
WHERE id = $1 AND state = 'PENDING'`
	if _, err := r.tx.Querier(ctx).Exec(ctx, q, id, truncateError(lastErr)); err != nil {
		return fmt.Errorf("mark outbox message failed: %w", err)
	}
	return nil
}

// PendingCount reports how many decisions still wait for delivery.
func (r *OutboxRepository) PendingCount(ctx context.Context) (int, error) {
	const q = `SELECT count(*) FROM outbox_messages WHERE state = 'PENDING'`
	var n int
	if err := r.tx.Querier(ctx).QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending outbox messages: %w", err)
	}
	return n, nil
}

// maxStoredErrorLength bounds what is written to last_error: a diagnostic
// column must not grow without limit.
const maxStoredErrorLength = 500

func truncateError(msg string) string {
	runes := []rune(msg)
	if len(runes) <= maxStoredErrorLength {
		return msg
	}
	return string(runes[:maxStoredErrorLength])
}

var _ app.OutboxRepository = (*OutboxRepository)(nil)
