// Package app holds the use cases of merchant-service. It depends on the
// domain and on the ports declared here; adapters implement the ports. It
// knows nothing about HTTP, gRPC or SQL.
package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/google/uuid"
)

// MerchantRepository is the storage port for merchants.
type MerchantRepository interface {
	// List returns one page of merchants matching the filter and the total
	// number of matches.
	List(ctx context.Context, f domain.ListFilter) ([]domain.Merchant, int, error)
	// GetByID returns one merchant or domain.ErrMerchantNotFound.
	GetByID(ctx context.Context, id uuid.UUID) (domain.Merchant, error)
	// GetByIDs returns the merchants that exist among ids, in no particular
	// order. Unknown ids are omitted.
	GetByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Merchant, error)
	// UpdateProfile applies the patch atomically. When expectedVersion is not
	// nil the update is conditional and returns domain.ErrVersionConflict if
	// the stored version differs.
	UpdateProfile(ctx context.Context, id uuid.UUID, patch domain.ProfilePatch,
		expectedVersion *int64) (domain.Merchant, error)
}

// APIKeyRepository is the storage port for merchant access keys.
type APIKeyRepository interface {
	// ResolveMerchant returns the merchant owning an active key with the given
	// hash, or domain.ErrAPIKeyNotFound.
	ResolveMerchant(ctx context.Context, keyHash []byte) (domain.Merchant, error)
}
