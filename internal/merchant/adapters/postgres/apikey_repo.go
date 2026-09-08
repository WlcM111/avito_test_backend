package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/jackc/pgx/v5"
)

// APIKeyRepository resolves merchant access keys.
type APIKeyRepository struct {
	tx *pgxx.TxManager
}

// NewAPIKeyRepository returns a repository bound to the transaction manager.
func NewAPIKeyRepository(tx *pgxx.TxManager) *APIKeyRepository {
	return &APIKeyRepository{tx: tx}
}

// ResolveMerchant returns the merchant owning an active key with the given
// hash. The join keeps authentication at one round trip, which matters because
// every merchant-facing request of every service goes through it.
func (r *APIKeyRepository) ResolveMerchant(ctx context.Context, keyHash []byte) (domain.Merchant, error) {
	const q = `
SELECT m.id, m.slug, m.name, m.description, m.cuisine, m.city, m.address, m.phone,
       m.status, m.is_accepting_orders, m.min_order_minor, m.delivery_fee_minor,
       m.avg_prep_minutes, m.version, m.created_at, m.updated_at
FROM merchant_api_keys k
JOIN merchants m ON m.id = k.merchant_id
WHERE k.key_hash = $1 AND k.is_active`

	row := r.tx.Querier(ctx).QueryRow(ctx, q, keyHash)
	m, err := scanMerchantRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Merchant{}, domain.ErrAPIKeyNotFound
	}
	if err != nil {
		return domain.Merchant{}, fmt.Errorf("resolve api key: %w", err)
	}
	return m, nil
}
