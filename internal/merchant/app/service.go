package app

import (
	"context"
	"fmt"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/google/uuid"
)

// Service implements the use cases of merchant-service.
type Service struct {
	merchants MerchantRepository
	keys      APIKeyRepository
	maxBatch  int
}

// NewService wires the use cases to their storage ports. maxBatch bounds the
// number of ids accepted by GetMerchants so that one caller cannot ask for an
// unbounded query.
func NewService(merchants MerchantRepository, keys APIKeyRepository, maxBatch int) *Service {
	if maxBatch <= 0 {
		maxBatch = 100
	}
	return &Service{merchants: merchants, keys: keys, maxBatch: maxBatch}
}

// ListPage is one page of the public merchant list.
type ListPage struct {
	Items  []domain.Merchant
	Total  int
	Limit  int
	Offset int
}

// ListMerchants returns the public merchant list.
func (s *Service) ListMerchants(ctx context.Context, f domain.ListFilter) (ListPage, error) {
	if err := f.Validate(); err != nil {
		return ListPage{}, err
	}
	items, total, err := s.merchants.List(ctx, f)
	if err != nil {
		return ListPage{}, fmt.Errorf("list merchants: %w", err)
	}
	return ListPage{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset}, nil
}

// GetMerchant returns one merchant by id.
func (s *Service) GetMerchant(ctx context.Context, id uuid.UUID) (domain.Merchant, error) {
	m, err := s.merchants.GetByID(ctx, id)
	if err != nil {
		return domain.Merchant{}, err
	}
	return m, nil
}

// GetMerchants returns the merchants that exist among ids. Duplicates in the
// input are collapsed. Unknown ids are omitted rather than failing the call,
// so a caller enriching a list of orders is not blocked by one stale id.
func (s *Service) GetMerchants(ctx context.Context, ids []uuid.UUID) ([]domain.Merchant, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) > s.maxBatch {
		ve := &domain.ValidationError{}
		ve.Add("merchant_ids", fmt.Sprintf("must not contain more than %d unique ids", s.maxBatch))
		return nil, ve
	}
	items, err := s.merchants.GetByIDs(ctx, unique)
	if err != nil {
		return nil, fmt.Errorf("get merchants by ids: %w", err)
	}
	return items, nil
}

// Authenticate resolves a raw access key to its merchant. It returns
// domain.ErrAPIKeyNotFound for an unknown, malformed or revoked key, without
// distinguishing the cases to the caller.
func (s *Service) Authenticate(ctx context.Context, rawKey string) (domain.Merchant, error) {
	if !domain.ValidAPIKeyFormat(rawKey) {
		return domain.Merchant{}, domain.ErrAPIKeyNotFound
	}
	m, err := s.keys.ResolveMerchant(ctx, domain.HashAPIKey(rawKey))
	if err != nil {
		return domain.Merchant{}, err
	}
	return m, nil
}

// UpdateProfile validates and applies a partial profile update.
// expectedVersion enables optimistic concurrency control: when it is not nil
// and does not match the stored version the update is rejected with
// domain.ErrVersionConflict instead of silently overwriting a concurrent
// change.
func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID,
	patch domain.ProfilePatch, expectedVersion *int64) (domain.Merchant, error) {
	if err := patch.Validate(); err != nil {
		return domain.Merchant{}, err
	}
	m, err := s.merchants.UpdateProfile(ctx, id, patch, expectedVersion)
	if err != nil {
		return domain.Merchant{}, err
	}
	return m, nil
}
