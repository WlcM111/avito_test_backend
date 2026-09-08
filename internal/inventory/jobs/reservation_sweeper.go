// Package jobs holds the background workers of inventory-service.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/app"
)

// ReservationSweeper releases holds whose TTL has passed.
//
// It is the second line of defence behind the normal release path: if
// order-service fails between taking a hold and recording the order, or if a
// terminal decision never reaches us, the held units would otherwise stay
// withdrawn from availability forever. The sweeper is safe to run on several
// instances at once because the repository picks its batch with
// FOR UPDATE SKIP LOCKED and every transition is conditional on the hold still
// being HELD.
type ReservationSweeper struct {
	svc      *app.Service
	logger   *slog.Logger
	interval time.Duration
	batch    int
	timeout  time.Duration
}

// NewReservationSweeper builds the worker.
func NewReservationSweeper(svc *app.Service, logger *slog.Logger,
	interval time.Duration, batch int, timeout time.Duration) *ReservationSweeper {
	if interval <= 0 {
		interval = time.Minute
	}
	if batch <= 0 {
		batch = 100
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &ReservationSweeper{
		svc: svc, logger: logger, interval: interval, batch: batch, timeout: timeout,
	}
}

// Run sweeps until ctx is cancelled. A failed pass is logged and retried on the
// next tick: the work is idempotent, so a transient database failure must not
// stop the worker.
func (s *ReservationSweeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Info("reservation sweeper started",
		slog.Duration("interval", s.interval), slog.Int("batch", s.batch))
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("reservation sweeper stopped")
			return nil
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// Sweep runs one pass. It is exported so that tests can drive the worker
// deterministically instead of waiting for a tick.
func (s *ReservationSweeper) Sweep(ctx context.Context) (int, error) {
	return s.svc.ReleaseExpired(ctx, time.Now().UTC(), s.batch)
}

func (s *ReservationSweeper) sweep(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	released, err := s.Sweep(passCtx)
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		return
	case err != nil:
		s.logger.Error("reservation sweep failed", slog.Any("error", err))
	case released > 0:
		s.logger.Info("expired reservations released", slog.Int("count", released))
	}
}
