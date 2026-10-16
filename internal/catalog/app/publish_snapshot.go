package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/google/uuid"
)

// PublishSnapshot replaces the menu of a merchant with the snapshot it sends.
//
// The snapshot is normalized and fully validated before it reaches storage, so
// a rejected publication changes nothing and reports every problem at once.
// expectedVersion carries the If-Match precondition: when it is not nil the
// publication only applies to the version the merchant last read, which is
// what stops two point-of-sale terminals from overwriting each other.
func (s *Service) PublishSnapshot(ctx context.Context, merchantID uuid.UUID,
	snapshot domain.MenuSnapshot, expectedVersion *int64) (PublishResult, error) {
	snapshot.MerchantID = merchantID
	normalized := snapshot.Normalize()
	if err := normalized.Validate(); err != nil {
		return PublishResult{}, err
	}
	if expectedVersion != nil && *expectedVersion < 0 {
		ve := &domain.ValidationError{}
		ve.Add("If-Match", "must not be negative")
		return PublishResult{}, ve
	}
	return s.menus.PublishSnapshot(ctx, normalized, expectedVersion)
}

// UpdateItemResult is one changed item together with the menu version the
// change produced.
type UpdateItemResult struct {
	Item        domain.MenuItem
	MenuVersion int64
}

// UpdateMenuItem changes the name or the price of one published item without
// republishing the whole menu. The menu version is bumped because a price is
// part of the menu content that order-service quotes against.
func (s *Service) UpdateMenuItem(ctx context.Context, merchantID, itemID uuid.UUID,
	patch domain.ItemPatch, expectedVersion *int64) (UpdateItemResult, error) {
	if itemID == uuid.Nil {
		ve := &domain.ValidationError{}
		ve.Add("item_id", "is required")
		return UpdateItemResult{}, ve
	}
	patch = patch.Normalize()
	if err := patch.Validate(); err != nil {
		return UpdateItemResult{}, err
	}
	item, version, err := s.menus.UpdateItem(ctx, merchantID, itemID, patch, expectedVersion)
	if err != nil {
		return UpdateItemResult{}, err
	}
	return UpdateItemResult{Item: item, MenuVersion: version}, nil
}
