package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

// applyDecision performs one decision against the platform. It returns the
// action that was actually carried out, or ActionHold when nothing was done.
func (s *Service) applyDecision(ctx context.Context, order Order,
	decision domain.Decision) (domain.Action, error) {
	switch decision.Action {
	case domain.ActionAccept:
		if err := s.platform.AcceptOrder(ctx, order.ID, decision.ETAMinutes); err != nil {
			return domain.ActionHold, err
		}
		s.logger.Info("order accepted",
			slog.String("order_id", order.ID),
			slog.Int("eta_minutes", int(decision.ETAMinutes)))
		return domain.ActionAccept, nil
	case domain.ActionReject:
		if err := s.platform.RejectOrder(ctx, order.ID, decision.Reason); err != nil {
			return domain.ActionHold, err
		}
		s.logger.Info("order refused",
			slog.String("order_id", order.ID), slog.String("reason", decision.Reason))
		return domain.ActionReject, nil
	case domain.ActionAdvance:
		if err := s.platform.AdvanceOrder(ctx, order.ID, decision.NextStatus); err != nil {
			return domain.ActionHold, err
		}
		s.logger.Info("order advanced",
			slog.String("order_id", order.ID), slog.String("status", decision.NextStatus))
		return domain.ActionAdvance, nil
	default:
		return domain.ActionHold, nil
	}
}

// statusAge is how long the order has been in its current status. The platform
// reports updated_at in RFC 3339; an unparsable value is treated as "just
// changed", so the till waits one more tick instead of racing ahead.
func statusAge(order Order, now time.Time) time.Duration {
	updated, err := time.Parse(time.RFC3339, order.UpdatedAt)
	if err != nil {
		return 0
	}
	age := now.Sub(updated)
	if age < 0 {
		return 0
	}
	return age
}
