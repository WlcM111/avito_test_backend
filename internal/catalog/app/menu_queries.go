package app

import (
	"context"
	"errors"

	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/google/uuid"
)

// GetPublicMenu returns the published menu shown to a customer. A merchant
// that has never published a menu is reported as not found: the catalogue does
// not own merchant identity and can only answer for menus it holds.
//
// The menu carries no availability. Whether a position can actually be ordered
// depends on the number of units, which belongs to inventory-service; a client
// combines this response with the availability endpoint of that service.
func (s *Service) GetPublicMenu(ctx context.Context, merchantID uuid.UUID) (domain.Menu, error) {
	if merchantID == uuid.Nil {
		ve := &domain.ValidationError{}
		ve.Add("merchant_id", "is required")
		return domain.Menu{}, ve
	}
	return s.menus.GetMenu(ctx, merchantID)
}

// GetOwnMenu returns the menu of the authenticated merchant. Unlike the public
// view it answers before the first publication as well, with an empty menu at
// version zero, because the merchant needs to see that state to leave it.
func (s *Service) GetOwnMenu(ctx context.Context, merchantID uuid.UUID) (domain.Menu, error) {
	menu, err := s.menus.GetMenu(ctx, merchantID)
	if errors.Is(err, domain.ErrMenuNotFound) {
		return domain.Menu{MerchantID: merchantID, MenuVersion: 0}, nil
	}
	if err != nil {
		return domain.Menu{}, err
	}
	return menu, nil
}
