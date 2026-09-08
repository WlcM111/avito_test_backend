package app

import (
	"context"
	"log/slog"
)

// PublishResult reports what one publication did.
type PublishResult struct {
	MenuVersion int64
	Items       int
	// Matched is how many till positions now carry a platform identifier.
	// A position the catalogue did not return stays unmatched, and its stock
	// cannot be synchronised until it does.
	Matched int
}

// PublishMenu sends the till menu to catalog-service and reads it back.
//
// The read-back is not a formality: the till addresses its positions by its
// own external identifier, while inventory-service and the order queue speak
// in platform identifiers. The mapping between the two exists only in the
// published menu, so it has to be fetched before any stock can be pushed.
func (s *Service) PublishMenu(ctx context.Context) (PublishResult, error) {
	s.mu.RLock()
	menu := s.menu.Clone()
	s.mu.RUnlock()

	if err := menu.Validate(); err != nil {
		return PublishResult{}, err
	}
	if _, err := s.platform.PublishMenu(ctx, menu); err != nil {
		return PublishResult{}, err
	}
	version, published, err := s.platform.FetchMenu(ctx)
	if err != nil {
		return PublishResult{}, err
	}

	byExternal := make(map[string]string, len(published))
	for _, item := range published {
		byExternal[item.ExternalID] = item.MenuItemID
	}

	s.mu.Lock()
	matched := 0
	for i := range s.menu.Items {
		if id, ok := byExternal[s.menu.Items[i].ExternalID]; ok {
			s.menu.Items[i].MenuItemID = id
			matched++
		}
	}
	s.published = true
	s.version = version
	total := len(s.menu.Items)
	s.mu.Unlock()

	s.logger.Info("menu published",
		slog.Int64("menu_version", version),
		slog.Int("items", total),
		slog.Int("matched", matched))
	return PublishResult{MenuVersion: version, Items: total, Matched: matched}, nil
}
