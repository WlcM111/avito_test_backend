package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

// PollResult reports what one poll of the queue did.
type PollResult struct {
	Seen     int
	Accepted int
	Rejected int
	Advanced int
	Held     int
}

// PollOrders asks the platform for the orders the till can still act on and
// applies the current behaviour to each of them (scenarios S-M6…S-M8).
//
// The pass is safe to repeat: the decision is derived from the status the
// platform reports, and the platform answers a repeated transition with the
// unchanged order. The till therefore keeps no state about orders at all.
func (s *Service) PollOrders(ctx context.Context, now time.Time) (PollResult, error) {
	behavior := s.Behavior()
	orders, err := s.platform.ListOrders(ctx, domain.WatchedStatuses(), s.cfg.OrderPageLimit)
	if err != nil {
		return PollResult{}, err
	}

	result := PollResult{Seen: len(orders)}
	for _, order := range orders {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		decision := domain.Decide(behavior, order.Status, statusAge(order, now), s.cfg.KitchenStep)
		action, applyErr := s.applyDecision(ctx, order, decision)
		if applyErr != nil {
			// One refused order must not stop the queue: the rest may still be
			// answerable, and this one comes back on the next tick.
			s.logger.Warn("order decision failed",
				slog.String("order_id", order.ID),
				slog.String("status", order.Status),
				slog.Any("error", applyErr))
			result.Held++
			continue
		}
		switch action {
		case domain.ActionAccept:
			result.Accepted++
		case domain.ActionReject:
			result.Rejected++
		case domain.ActionAdvance:
			result.Advanced++
		default:
			result.Held++
		}
	}
	return result, nil
}

// Orders returns the orders of the outlet the till currently watches. It is a
// read for the control API and changes nothing.
func (s *Service) Orders(ctx context.Context) ([]Order, error) {
	return s.platform.ListOrders(ctx, domain.WatchedStatuses(), s.cfg.OrderPageLimit)
}

// RunOrderPolling polls the queue until ctx is cancelled.
func (s *Service) RunOrderPolling(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	s.logger.Info("order poller started", slog.Duration("interval", s.cfg.PollInterval))
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("order poller stopped")
			return nil
		case <-ticker.C:
			result, err := s.PollOrders(ctx, time.Now().UTC())
			switch {
			case err != nil && errors.Is(err, context.Canceled):
				continue
			case err != nil:
				s.logger.Warn("order poll failed", slog.Any("error", err))
			case result.Accepted+result.Rejected+result.Advanced > 0:
				s.logger.Info("order poll finished",
					slog.Int("seen", result.Seen),
					slog.Int("accepted", result.Accepted),
					slog.Int("rejected", result.Rejected),
					slog.Int("advanced", result.Advanced))
			}
		}
	}
}
