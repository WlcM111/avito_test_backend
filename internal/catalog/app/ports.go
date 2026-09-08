// Package app holds the use cases of catalog-service. It depends on the domain
// and on the ports declared here; adapters implement the ports. It knows
// nothing about HTTP, gRPC or SQL.
package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/google/uuid"
)

// PublishResult reports the outcome of one publication.
type PublishResult struct {
	// Menu is the menu as it is after the publication.
	Menu domain.Menu
	// Created, Updated and Withdrawn count the items the publication added,
	// changed and removed from sale. They are reported back so that a merchant
	// integration can see what its export actually did.
	Created   int
	Updated   int
	Withdrawn int
}

// MenuRepository is the storage port of the catalogue.
//
// Publication is one transaction by contract: the version increment, the
// upsert of the published items and the withdrawal of the absent ones either
// all happen or none do. A partially applied menu would let a merchant keep
// selling a position it has already removed.
type MenuRepository interface {
	// GetMenu returns the published menu of a merchant, or
	// domain.ErrMenuNotFound when the merchant has never published one.
	// The menu and its version are read in one snapshot.
	GetMenu(ctx context.Context, merchantID uuid.UUID) (domain.Menu, error)
	// PublishSnapshot replaces the menu of a merchant with the snapshot.
	// expectedVersion, when not nil, makes the publication conditional and
	// returns domain.ErrVersionConflict if the stored version differs.
	PublishSnapshot(ctx context.Context, snapshot domain.MenuSnapshot,
		expectedVersion *int64) (PublishResult, error)
	// UpdateItem applies a partial change to one published item and bumps the
	// menu version, because the content of the menu changed. It returns
	// domain.ErrMenuItemNotFound for an unknown or withdrawn item and
	// domain.ErrVersionConflict for a stale expectedVersion.
	UpdateItem(ctx context.Context, merchantID, itemID uuid.UUID,
		patch domain.ItemPatch, expectedVersion *int64) (domain.MenuItem, int64, error)
	// QuoteItems resolves menu item ids to their current names and prices. It
	// returns one quote per requested id, in the requested order, and the menu
	// version the quotes were read at. Unknown, foreign and withdrawn items
	// carry a problem instead of failing the call.
	QuoteItems(ctx context.Context, merchantID uuid.UUID,
		menuItemIDs []uuid.UUID) (int64, []domain.Quote, error)
}

// MerchantIdentity is what the merchant directory tells us about the caller.
type MerchantIdentity struct {
	MerchantID uuid.UUID
	Active     bool
}

// MerchantDirectory is the port to merchant-service. catalog-service does not
// own merchant identity and must ask its owner on every merchant request.
type MerchantDirectory interface {
	// Authenticate resolves an access key to its merchant, or returns
	// ErrMerchantUnauthenticated. A dependency failure returns
	// ErrMerchantDirectoryUnavailable so that the transport can answer 503
	// rather than 401.
	Authenticate(ctx context.Context, apiKey string) (MerchantIdentity, error)
}
