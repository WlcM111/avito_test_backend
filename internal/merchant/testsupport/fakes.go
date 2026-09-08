// Package testsupport provides in-memory implementations of the merchant
// storage ports. They exist so that the use-case and transport layers can be
// tested without a database. They are used by tests only; the SQL behaviour
// itself is covered by the integration tests against a real PostgreSQL.
package testsupport

import (
	"context"
	"sync"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/google/uuid"
)

// MerchantRepo is an in-memory domain-faithful MerchantRepository.
type MerchantRepo struct {
	mu          sync.Mutex
	Items       map[uuid.UUID]domain.Merchant
	LastFilter  domain.ListFilter
	UpdateCalls int
	ListErr     error
}

// NewMerchantRepo seeds the repository with the given merchants.
func NewMerchantRepo(items ...domain.Merchant) *MerchantRepo {
	r := &MerchantRepo{Items: map[uuid.UUID]domain.Merchant{}}
	for _, m := range items {
		r.Items[m.ID] = m
	}
	return r
}

// List returns every stored merchant and records the filter it was called with.
func (r *MerchantRepo) List(_ context.Context, f domain.ListFilter) ([]domain.Merchant, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastFilter = f
	if r.ListErr != nil {
		return nil, 0, r.ListErr
	}
	out := make([]domain.Merchant, 0, len(r.Items))
	for _, m := range r.Items {
		out = append(out, m)
	}
	return out, len(out), nil
}

// GetByID returns one merchant or domain.ErrMerchantNotFound.
func (r *MerchantRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Merchant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.Items[id]
	if !ok {
		return domain.Merchant{}, domain.ErrMerchantNotFound
	}
	return m, nil
}

// GetByIDs returns the merchants that exist among ids.
func (r *MerchantRepo) GetByIDs(_ context.Context, ids []uuid.UUID) ([]domain.Merchant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Merchant
	for _, id := range ids {
		if m, ok := r.Items[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// UpdateProfile mirrors the conditional UPDATE of the SQL adapter: it honours
// the expected version and increments the stored one.
func (r *MerchantRepo) UpdateProfile(_ context.Context, id uuid.UUID,
	patch domain.ProfilePatch, expected *int64) (domain.Merchant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.UpdateCalls++
	m, ok := r.Items[id]
	if !ok {
		return domain.Merchant{}, domain.ErrMerchantNotFound
	}
	if expected != nil && *expected != m.Version {
		return domain.Merchant{}, domain.ErrVersionConflict
	}
	m.Apply(patch)
	m.Version++
	r.Items[id] = m
	return m, nil
}

// APIKeyRepo is an in-memory APIKeyRepository keyed by the key digest.
type APIKeyRepo struct {
	ByHash map[string]domain.Merchant
}

// NewAPIKeyRepo maps raw development keys to their merchants.
func NewAPIKeyRepo(pairs map[string]domain.Merchant) *APIKeyRepo {
	byHash := make(map[string]domain.Merchant, len(pairs))
	for raw, m := range pairs {
		byHash[string(domain.HashAPIKey(raw))] = m
	}
	return &APIKeyRepo{ByHash: byHash}
}

// ResolveMerchant returns the merchant owning the key digest.
func (r *APIKeyRepo) ResolveMerchant(_ context.Context, hash []byte) (domain.Merchant, error) {
	m, ok := r.ByHash[string(hash)]
	if !ok {
		return domain.Merchant{}, domain.ErrAPIKeyNotFound
	}
	return m, nil
}

// SampleMerchant returns a merchant matching the development seed.
func SampleMerchant() domain.Merchant {
	return domain.Merchant{
		ID:                uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		Slug:              "kafe-primer",
		Name:              "Кафе «Пример»",
		Description:       "Демонстрационное заведение",
		Cuisine:           "Домашняя кухня",
		City:              "Санкт-Петербург",
		Address:           "Невский проспект, 1",
		Phone:             "+78120000001",
		Status:            domain.StatusActive,
		IsAcceptingOrders: true,
		MinOrderMinor:     50000,
		DeliveryFeeMinor:  19900,
		AvgPrepMinutes:    35,
		Version:           1,
	}
}

// SuspendedMerchant returns a merchant disconnected by the operator.
func SuspendedMerchant() domain.Merchant {
	m := SampleMerchant()
	m.ID = uuid.MustParse("44444444-4444-4444-8444-444444444444")
	m.Slug = "burger-otklyuchen"
	m.Name = "Бургерная «Отключена»"
	m.Status = domain.StatusSuspended
	m.IsAcceptingOrders = false
	return m
}

// DevKey is the development access key of SampleMerchant.
const DevKey = "dev-merchant-key-kafe-primer-0001"
