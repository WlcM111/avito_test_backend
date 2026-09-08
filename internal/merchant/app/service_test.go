package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/google/uuid"
)

// fakeMerchantRepo is a test double for the storage port. Doubles are used
// only at this level: the SQL behaviour itself is covered by the integration
// tests against a real PostgreSQL instance.
type fakeMerchantRepo struct {
	items       map[uuid.UUID]domain.Merchant
	lastFilter  domain.ListFilter
	updateCalls int
	updateErr   error
	listErr     error
}

func newFakeRepo(items ...domain.Merchant) *fakeMerchantRepo {
	r := &fakeMerchantRepo{items: map[uuid.UUID]domain.Merchant{}}
	for _, m := range items {
		r.items[m.ID] = m
	}
	return r
}

func (r *fakeMerchantRepo) List(_ context.Context, f domain.ListFilter) ([]domain.Merchant, int, error) {
	r.lastFilter = f
	if r.listErr != nil {
		return nil, 0, r.listErr
	}
	out := make([]domain.Merchant, 0, len(r.items))
	for _, m := range r.items {
		out = append(out, m)
	}
	return out, len(out), nil
}

func (r *fakeMerchantRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Merchant, error) {
	m, ok := r.items[id]
	if !ok {
		return domain.Merchant{}, domain.ErrMerchantNotFound
	}
	return m, nil
}

func (r *fakeMerchantRepo) GetByIDs(_ context.Context, ids []uuid.UUID) ([]domain.Merchant, error) {
	var out []domain.Merchant
	for _, id := range ids {
		if m, ok := r.items[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (r *fakeMerchantRepo) UpdateProfile(_ context.Context, id uuid.UUID,
	patch domain.ProfilePatch, expected *int64) (domain.Merchant, error) {
	r.updateCalls++
	if r.updateErr != nil {
		return domain.Merchant{}, r.updateErr
	}
	m, ok := r.items[id]
	if !ok {
		return domain.Merchant{}, domain.ErrMerchantNotFound
	}
	if expected != nil && *expected != m.Version {
		return domain.Merchant{}, domain.ErrVersionConflict
	}
	m.Apply(patch)
	m.Version++
	r.items[id] = m
	return m, nil
}

type fakeKeyRepo struct {
	byHash map[string]domain.Merchant
}

func (r *fakeKeyRepo) ResolveMerchant(_ context.Context, hash []byte) (domain.Merchant, error) {
	m, ok := r.byHash[string(hash)]
	if !ok {
		return domain.Merchant{}, domain.ErrAPIKeyNotFound
	}
	return m, nil
}

func ptr[T any](v T) *T { return &v }

func sampleMerchant() domain.Merchant {
	return domain.Merchant{
		ID:                uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		Slug:              "kafe-primer",
		Name:              "Кафе «Пример»",
		City:              "Санкт-Петербург",
		Status:            domain.StatusActive,
		IsAcceptingOrders: true,
		MinOrderMinor:     50000,
		DeliveryFeeMinor:  19900,
		AvgPrepMinutes:    35,
		Version:           1,
	}
}

func TestListMerchantsRejectsInvalidFilter(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	svc := app.NewService(repo, &fakeKeyRepo{}, 100)

	_, err := svc.ListMerchants(context.Background(), domain.ListFilter{Limit: 0})
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("err = %v, want validation error", err)
	}
	if repo.lastFilter.Limit != 0 && len(repo.items) != 0 {
		t.Fatal("repository was queried with an invalid filter")
	}
}

func TestListMerchantsPassesFilterThrough(t *testing.T) {
	t.Parallel()
	m := sampleMerchant()
	repo := newFakeRepo(m)
	svc := app.NewService(repo, &fakeKeyRepo{}, 100)

	f := domain.ListFilter{City: "Санкт-Петербург", Query: "кафе", OnlyAccepting: true, Limit: 5, Offset: 10}
	page, err := svc.ListMerchants(context.Background(), f)
	if err != nil {
		t.Fatalf("ListMerchants() = %v", err)
	}
	if repo.lastFilter != f {
		t.Fatalf("filter passed to repository = %+v, want %+v", repo.lastFilter, f)
	}
	if page.Limit != 5 || page.Offset != 10 || page.Total != 1 {
		t.Fatalf("page = %+v", page)
	}
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()
	m := sampleMerchant()
	const rawKey = "dev-merchant-key-kafe-primer-0001"
	keys := &fakeKeyRepo{byHash: map[string]domain.Merchant{
		string(domain.HashAPIKey(rawKey)): m,
	}}
	svc := app.NewService(newFakeRepo(m), keys, 100)

	got, err := svc.Authenticate(context.Background(), rawKey)
	if err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
	if got.ID != m.ID {
		t.Fatalf("merchant id = %s, want %s", got.ID, m.ID)
	}

	for _, bad := range []string{"", "short", "dev-merchant-key-unknown-000000"} {
		if _, err := svc.Authenticate(context.Background(), bad); !errors.Is(err, domain.ErrAPIKeyNotFound) {
			t.Fatalf("Authenticate(%q) = %v, want ErrAPIKeyNotFound", bad, err)
		}
	}
}

func TestUpdateProfileValidatesBeforeStorage(t *testing.T) {
	t.Parallel()
	m := sampleMerchant()
	repo := newFakeRepo(m)
	svc := app.NewService(repo, &fakeKeyRepo{}, 100)

	_, err := svc.UpdateProfile(context.Background(), m.ID,
		domain.ProfilePatch{AvgPrepMinutes: ptr(int32(0))}, nil)
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("err = %v, want validation error", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("repository was called %d times for an invalid patch", repo.updateCalls)
	}
}

func TestUpdateProfileOptimisticConcurrency(t *testing.T) {
	t.Parallel()
	m := sampleMerchant()
	repo := newFakeRepo(m)
	svc := app.NewService(repo, &fakeKeyRepo{}, 100)

	updated, err := svc.UpdateProfile(context.Background(), m.ID,
		domain.ProfilePatch{IsAcceptingOrders: ptr(false)}, ptr(int64(1)))
	if err != nil {
		t.Fatalf("first update = %v", err)
	}
	if updated.Version != 2 || updated.IsAcceptingOrders {
		t.Fatalf("updated = %+v", updated)
	}

	// The caller repeats with the now stale version.
	_, err = svc.UpdateProfile(context.Background(), m.ID,
		domain.ProfilePatch{IsAcceptingOrders: ptr(true)}, ptr(int64(1)))
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale update = %v, want ErrVersionConflict", err)
	}
}

func TestUpdateProfileUnknownMerchant(t *testing.T) {
	t.Parallel()
	svc := app.NewService(newFakeRepo(), &fakeKeyRepo{}, 100)
	_, err := svc.UpdateProfile(context.Background(), uuid.New(),
		domain.ProfilePatch{IsAcceptingOrders: ptr(true)}, nil)
	if !errors.Is(err, domain.ErrMerchantNotFound) {
		t.Fatalf("err = %v, want ErrMerchantNotFound", err)
	}
}

func TestGetMerchantsCollapsesDuplicatesAndBounds(t *testing.T) {
	t.Parallel()
	m := sampleMerchant()
	svc := app.NewService(newFakeRepo(m), &fakeKeyRepo{}, 2)

	items, err := svc.GetMerchants(context.Background(), []uuid.UUID{m.ID, m.ID})
	if err != nil {
		t.Fatalf("GetMerchants() = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d merchants, want 1 (duplicates collapsed)", len(items))
	}

	if _, err := svc.GetMerchants(context.Background(), nil); err != nil {
		t.Fatalf("GetMerchants(nil) = %v", err)
	}

	tooMany := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	if _, err := svc.GetMerchants(context.Background(), tooMany); err == nil {
		t.Fatal("batch above the limit was accepted")
	}
}

func TestGetMerchantsIgnoresUnknownIDs(t *testing.T) {
	t.Parallel()
	m := sampleMerchant()
	svc := app.NewService(newFakeRepo(m), &fakeKeyRepo{}, 100)

	items, err := svc.GetMerchants(context.Background(), []uuid.UUID{m.ID, uuid.New()})
	if err != nil {
		t.Fatalf("GetMerchants() = %v", err)
	}
	if len(items) != 1 || items[0].ID != m.ID {
		t.Fatalf("items = %+v, want only the known merchant", items)
	}
}
