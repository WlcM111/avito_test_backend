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
	s.mu.Lock()
	items := s.menu.Clone().Items
	published := s.published
	if published {
		// Снимаем признак до выталкивания, а не после: изменение, пришедшее
		// через PATCH во время выталкивания, снова его поднимет и уедет
		// следующим проходом. Снятие после привело бы к потере изменения.
		s.pendingSync = false
	}
	s.mu.Unlock()

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
			s.markPending()
			return result, err
		}
		if err := s.platform.SetStock(ctx, item.MenuItemID, item.OnHandQty, "pos sync"); err != nil {
			s.markPending()
			return result, err
		}
		if err := s.platform.SetSalesState(ctx, item.MenuItemID, item.SalesStopped); err != nil {
			s.markPending()
			return result, err
		}
		result.Updated++
	}

	s.logger.Info("stock synchronised",
		slog.Int("updated", result.Updated), slog.Int("skipped", result.Skipped))
	return result, nil
}

// markPending возвращает признак незавершённой синхронизации: выталкивание
// оборвалось, и изменения кассы до платформы не доехали.
func (s *Service) markPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingSync = true
}

// syncPass доводит платформу до состояния кассы за один проход: при
// необходимости публикует меню и выталкивает изменения остатков.
//
// Публикация повторяется до успеха, потому что без неё касса не знает
// платформенных идентификаторов своих позиций и не может выставить ни одной
// штуки. Остатки выталкиваются только когда касса сама что-то меняла.
func (s *Service) syncPass(ctx context.Context) {
	if s.cfg.AutoPublish && !s.Menu().Published {
		if _, err := s.PublishMenu(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				s.logger.Warn("menu publication failed, will retry",
					slog.Any("error", err))
			}
			return
		}
	}
	if !s.pendingChanges() {
		return
	}
	if _, err := s.SyncStock(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logger.Warn("stock synchronisation failed, will retry",
				slog.Any("error", err))
		}
	}
}

// nextSyncDelay: пока меню не опубликовано, касса бесполезна для платформы,
// поэтому повторяет чаще — с интервалом опроса очереди, а не раз в минуту.
func (s *Service) nextSyncDelay() time.Duration {
	if s.cfg.AutoPublish && !s.Menu().Published {
		return s.cfg.PollInterval
	}
	return s.cfg.StockSyncInterval
}

// RunStockSync keeps the platform in step with the till until ctx is
// cancelled. Первый проход выполняется сразу, без ожидания тика: именно он
// публикует меню при старте. Ошибка прохода логируется и повторяется — работа
// идемпотентна, и временная недоступность платформы не должна останавливать
// кассу навсегда.
func (s *Service) RunStockSync(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()

	s.logger.Info("stock synchroniser started",
		slog.Duration("interval", s.cfg.StockSyncInterval),
		slog.Bool("auto_publish", s.cfg.AutoPublish))
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("stock synchroniser stopped")
			return nil
		case <-timer.C:
			s.syncPass(ctx)
			timer.Reset(s.nextSyncDelay())
		}
	}
}
