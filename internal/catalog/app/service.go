package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/catalog/domain"
)

// Config bounds the use cases. The values come from the service configuration
// and are validated at start-up.
type Config struct {
	MaxQuoteBatch int
}

// Service implements the use cases of catalog-service.
type Service struct {
	menus     MenuRepository
	merchants MerchantDirectory
	cfg       Config
}

// NewService wires the use cases to their ports.
func NewService(menus MenuRepository, merchants MerchantDirectory, cfg Config) *Service {
	if cfg.MaxQuoteBatch <= 0 {
		cfg.MaxQuoteBatch = domain.MaxQuoteBatch
	}
	return &Service{menus: menus, merchants: merchants, cfg: cfg}
}

// Authenticate resolves a merchant access key through merchant-service.
func (s *Service) Authenticate(ctx context.Context, apiKey string) (MerchantIdentity, error) {
	if apiKey == "" {
		return MerchantIdentity{}, ErrMerchantUnauthenticated
	}
	identity, err := s.merchants.Authenticate(ctx, apiKey)
	if err != nil {
		return MerchantIdentity{}, err
	}
	// A merchant disconnected by the operator keeps a technically valid key
	// but must not change its menu, so the check belongs here rather than in
	// transport.
	if !identity.Active {
		return MerchantIdentity{}, ErrMerchantUnauthenticated
	}
	return identity, nil
}
