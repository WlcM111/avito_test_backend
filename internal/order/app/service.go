package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/internal/order/domain"
)

// Config bounds the use cases. The values come from the service configuration
// and are validated at start-up.
type Config struct {
	// ReservationTTL is how long inventory-service holds the units of a new
	// order. It must stay comfortably above the whole outbox retry window, so
	// that a lost release is corrected by the sweeper rather than by a human.
	ReservationTTL time.Duration
	// MaxDeliveryAttempts caps the delivery attempts of one outbox message.
	MaxDeliveryAttempts int32
}

// Service implements the use cases of order-service.
type Service struct {
	orders    OrderRepository
	outbox    OutboxRepository
	merchants MerchantDirectory
	catalogue MenuCatalogue
	stock     StockLedger
	logger    *slog.Logger
	cfg       Config
}

// NewService wires the use cases to their ports.
func NewService(orders OrderRepository, outbox OutboxRepository,
	merchants MerchantDirectory, catalogue MenuCatalogue, stock StockLedger,
	logger *slog.Logger, cfg Config) *Service {
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = 30 * time.Minute
	}
	if cfg.MaxDeliveryAttempts <= 0 {
		cfg.MaxDeliveryAttempts = domain.MaxDeliveryAttempts
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		orders: orders, outbox: outbox, merchants: merchants,
		catalogue: catalogue, stock: stock, logger: logger, cfg: cfg,
	}
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
	// but must not work the order queue, so the check belongs here rather than
	// in transport.
	if !identity.Active {
		return MerchantIdentity{}, ErrMerchantUnauthenticated
	}
	return identity, nil
}
