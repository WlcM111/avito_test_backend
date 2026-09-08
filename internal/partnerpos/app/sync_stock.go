package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

// SyncResult reports what one stock synchronisation did.
type SyncResult struct {
	Updated int
	Skipped int
}

// SyncStock pushes the quantities of the till to inventory-service.
//
// Quantity and stop list are two separate calls by contract: the stop list
// must never confirm units, so inventory-service refuses to change both at
// once. A position without a platform identifier is skipped rather than
// guessed - the menu has to be published first.
func (s *Service) SyncStock(ctx context.Context) (SyncResult, error) {
	s.mu.RLock()
	items := s.menu.Clone().Items
	published := s.published
	s.mu.RUnlock()

	if !published {
		return SyncResult{}, domain.ErrNotPublished
	}

	var result SyncResult
	for _, item := range items {
		if item.MenuItemID == "" {
			result.Skipped++
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := s.platform.SetStock(ctx, item.MenuItemID, item.OnHandQty, "pos sync"); err != nil {
			return result, err
		}
		if err := s.platform.SetSalesState(ctx, item.MenuItemID, item.SalesStopped); err != nil {
			return result, err
		}
		result.Updated++
	}
	s.logger.Info("stock synchronised",
		slog.Int("updated", result.Updated), slog.Int("skipped", result.Skipped))
	return result, nil
}

// RunStockSync keeps the platform in step with the till until ctx is
// cancelled. A failed pass is logged and retried on the next tick: the work is
// idempotent, so a temporary outage of the platform must not stop the till.
func (s *Service) RunStockSync(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.StockSyncInterval)
	defer ticker.Stop()

	s.logger.Info("stock synchroniser started",
		slog.Duration("interval", s.cfg.StockSyncInterval))
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("stock synchroniser stopped")
			return nil
		case <-ticker.C:
			if _, err := s.SyncStock(ctx); err != nil {
				if errors.Is(err, context.Canceled) {
					continue
				}
				s.logger.Warn("stock synchronisation failed", slog.Any("error", err))
			}
		}
	}
}
