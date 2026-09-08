package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/logx"
)

// DispatchStats reports what one pass of the dispatcher did.
type DispatchStats struct {
	Claimed int
	Sent    int
	Retried int
	Failed  int
}

// DispatchOutbox delivers the terminal decisions waiting in the outbox
// (TZ v3.0 §8.3).
//
// The guarantee of the path is at-least-once delivery plus an idempotent
// receiver, which makes the *effect* happen once. Exactly-once delivery is not
// claimed and is not achievable here: the acknowledgement of the receiver and
// our own record of it are two different systems.
//
// A message is claimed in its own short transaction and only then sent, so no
// database transaction stays open across a network call.
func (s *Service) DispatchOutbox(ctx context.Context, now time.Time,
	batch int, lease time.Duration) (DispatchStats, error) {
	var stats DispatchStats
	messages, err := s.outbox.Claim(ctx, now, batch, lease)
	if err != nil {
		return stats, err
	}
	stats.Claimed = len(messages)

	for _, msg := range messages {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		outcome, deliverErr := s.deliver(ctx, msg)
		switch outcome {
		case deliveryDone:
			if markErr := s.outbox.MarkSent(ctx, msg.ID); markErr != nil {
				return stats, markErr
			}
			stats.Sent++
		case deliveryPermanentFailure:
			if markErr := s.outbox.MarkFailed(ctx, msg.ID, deliverErr.Error()); markErr != nil {
				return stats, markErr
			}
			stats.Failed++
			logx.From(ctx, s.logger).Error("outbox message parked",
				slog.String("message_id", msg.ID.String()),
				slog.String("order_id", msg.AggregateID.String()),
				slog.String("type", string(msg.Type)),
				slog.Int("attempts", int(msg.Attempts)),
				slog.Any("error", deliverErr))
		default:
			if msg.Attempts >= s.cfg.MaxDeliveryAttempts {
				if markErr := s.outbox.MarkFailed(ctx, msg.ID, deliverErr.Error()); markErr != nil {
					return stats, markErr
				}
				stats.Failed++
				logx.From(ctx, s.logger).Error("outbox message exhausted its attempts",
					slog.String("message_id", msg.ID.String()),
					slog.String("order_id", msg.AggregateID.String()),
					slog.Int("attempts", int(msg.Attempts)),
					slog.Any("error", deliverErr))
				continue
			}
			next := now.Add(domain.BackoffFor(msg.Attempts))
			if rescheduleErr := s.outbox.Reschedule(ctx, msg.ID, next, deliverErr.Error()); rescheduleErr != nil {
				return stats, rescheduleErr
			}
			stats.Retried++
			logx.From(ctx, s.logger).Warn("outbox delivery failed, retrying",
				slog.String("message_id", msg.ID.String()),
				slog.String("order_id", msg.AggregateID.String()),
				slog.Int("attempts", int(msg.Attempts)),
				slog.Time("next_attempt_at", next),
				slog.Any("error", deliverErr))
		}
	}
	return stats, nil
}

type deliveryOutcome int

const (
	deliveryRetryable deliveryOutcome = iota
	deliveryDone
	deliveryPermanentFailure
)

// deliver performs one message and classifies the outcome. The classification
// is the whole point of the retry policy: a malformed message and an
// unreachable receiver look the same to the caller but must be handled in
// opposite ways.
func (s *Service) deliver(ctx context.Context, msg domain.Message) (deliveryOutcome, error) {
	var err error
	switch msg.Type {
	case domain.MessageCommitReservation:
		err = s.stock.Commit(ctx, msg.ReservationID)
	case domain.MessageReleaseReservation:
		err = s.stock.Release(ctx, msg.ReservationID, msg.Reason)
	default:
		return deliveryPermanentFailure, errors.New("unknown outbox message type " + string(msg.Type))
	}
	switch {
	case err == nil:
		return deliveryDone, nil
	case errors.Is(err, ErrInvalidRequest):
		// The receiver says the message itself is wrong. Repeating an
		// identical message cannot change that answer.
		return deliveryPermanentFailure, err
	case errors.Is(err, ErrReservationNotFound):
		return deliveryPermanentFailure, err
	case errors.Is(err, ErrReservationNotHeld):
		// The hold already reached a terminal state, and not the one we asked
		// for - for example the TTL expired and the units were swept back
		// before the merchant's confirmation arrived. Retrying cannot fix it,
		// and hiding it as success would leave a confirmed order whose units
		// were returned, so it is parked for a human.
		return deliveryPermanentFailure, err
	default:
		return deliveryRetryable, err
	}
}

// PendingOutboxCount reports how many decisions still wait for delivery. It
// exists for the readiness and diagnostics paths, not for business logic.
func (s *Service) PendingOutboxCount(ctx context.Context) (int, error) {
	return s.outbox.PendingCount(ctx)
}
