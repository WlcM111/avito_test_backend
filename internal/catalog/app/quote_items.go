package app

import (
	"context"
	"fmt"

	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/google/uuid"
)

// QuoteResult is the answer of QuoteItems: the quotes and the menu version
// they were read at.
type QuoteResult struct {
	MenuVersion int64
	Items       []domain.Quote
}

// QuoteItems resolves menu item ids to their current names and prices for
// order-service.
//
// An unknown, foreign or withdrawn item is reported with its problem instead
// of failing the call: the order needs the outcome of every requested line at
// once, exactly as the availability check does.
func (s *Service) QuoteItems(ctx context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) (QuoteResult, error) {
	if merchantID == uuid.Nil {
		ve := &domain.ValidationError{}
		ve.Add("merchant_id", "is required")
		return QuoteResult{}, ve
	}
	unique := dedupeUUIDs(menuItemIDs)
	if len(unique) == 0 {
		ve := &domain.ValidationError{}
		ve.Add("menu_item_ids", "must contain at least one identifier")
		return QuoteResult{}, ve
	}
	if len(unique) > s.cfg.MaxQuoteBatch {
		ve := &domain.ValidationError{}
		ve.Add("menu_item_ids", fmt.Sprintf("must not contain more than %d identifiers",
			s.cfg.MaxQuoteBatch))
		return QuoteResult{}, ve
	}

	version, quotes, err := s.menus.QuoteItems(ctx, merchantID, unique)
	if err != nil {
		return QuoteResult{}, fmt.Errorf("quote items: %w", err)
	}
	return QuoteResult{MenuVersion: version, Items: quotes}, nil
}

func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
