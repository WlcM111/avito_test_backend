// Package jobs holds the background workers of order-service.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/internal/order/app"
)

// OutboxDispatcher delivers terminal decisions to inventory-service.
//
// It is the second half of the transactional outbox: the decision is written
// with the status change, and this worker is what makes it leave the database.
// Running several instances is safe because the repository claims a batch with
// FOR UPDATE SKIP LOCKED and the receiver is idempotent, so a message
// delivered twice has the effect once.
type OutboxDispatcher struct {
	svc      *app.Service
	logger   *slog.Logger
	interval time.Duration
	batch    int
	lease    time.Duration
	timeout  time.Duration
}

// NewOutboxDispatcher builds the worker.
func NewOutboxDispatcher(svc *app.Service, logger *slog.Logger,
	interval time.Duration, batch int, lease, timeout time.Duration) *OutboxDispatcher {
	if interval <= 0 {
		interval = time.Second
	}
	if batch <= 0 {
		batch = 50
	}
	if lease <= 0 {
		lease = time.Minute
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &OutboxDispatcher{
		svc: svc, logger: logger, interval: interval,
		batch: batch, lease: lease, timeout: timeout,
	}
}

// Run dispatches until ctx is cancelled. A failed pass is logged and retried on
// the next tick: the work is idempotent, so a transient database failure must
// not stop the worker.
func (d *OutboxDispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.logger.Info("outbox dispatcher started",
		slog.Duration("interval", d.interval), slog.Int("batch", d.batch))
	for {
		select {
		case <-ctx.Done():
			d.logger.Info("outbox dispatcher stopped")
			return nil
		case <-ticker.C:
			d.pass(ctx)
		}
	}
}

// Dispatch runs one pass. It is exported so that tests can drive the worker
// deterministically instead of waiting for a tick.
func (d *OutboxDispatcher) Dispatch(ctx context.Context) (app.DispatchStats, error) {
	return d.svc.DispatchOutbox(ctx, time.Now().UTC(), d.batch, d.lease)
}

func (d *OutboxDispatcher) pass(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	stats, err := d.Dispatch(passCtx)
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		return
	case err != nil:
		d.logger.Error("outbox pass failed", slog.Any("error", err))
	case stats.Claimed > 0:
		d.logger.Info("outbox pass finished",
			slog.Int("claimed", stats.Claimed),
			slog.Int("sent", stats.Sent),
			slog.Int("retried", stats.Retried),
			slog.Int("failed", stats.Failed))
	}
}
