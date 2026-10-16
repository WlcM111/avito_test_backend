package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pgstore "github.com/avito-kitchen/platform/internal/merchant/adapters/postgres"
	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/avito-kitchen/platform/test/testenv"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	seedKafePrimer = "11111111-1111-4111-8111-111111111111"
	seedSuspended  = "44444444-4444-4444-8444-444444444444"
	devKey         = "dev-merchant-key-kafe-primer-0001"
	revokedKey     = "dev-merchant-key-revoked-0009"
)

func ptr[T any](v T) *T { return &v }

func newRepos(t *testing.T) (*pgxpool.Pool, *pgstore.MerchantRepository, *pgstore.APIKeyRepository) {
	t.Helper()
	pool := testenv.RequireDB(t, migrations.DirMerchant)
	tx := pgxx.NewTxManager(pool)
	return pool, pgstore.NewMerchantRepository(tx), pgstore.NewAPIKeyRepository(tx)
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestMigrationsAreIdempotent(t *testing.T) {
	pool := testenv.RequireDB(t, migrations.DirMerchant)
	ctx := testCtx(t)

	before, err := pgmigrateStatus(ctx, pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	// Applying the same files again must be a no-op.
	if err := applyAgain(ctx, pool); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after, err := pgmigrateStatus(ctx, pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if before != after {
		t.Fatalf("version changed on re-apply: %d -> %d", before, after)
	}

	var merchants int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM merchants`).Scan(&merchants); err != nil {
		t.Fatalf("count merchants: %v", err)
	}
	if merchants != 4 {
		t.Fatalf("seeded merchants = %d, want 4 (seed must not be applied twice)", merchants)
	}
}

func TestListOnlyReturnsActiveMerchants(t *testing.T) {
	_, repo, _ := newRepos(t)
	ctx := testCtx(t)

	items, total, err := repo.List(ctx, domain.ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("got %d items (total %d), want 3 active merchants", len(items), total)
	}
	for _, m := range items {
		if m.Status != domain.StatusActive {
			t.Fatalf("suspended merchant %s leaked into the public list", m.Slug)
		}
	}
}

func TestListFiltersAndPagination(t *testing.T) {
	_, repo, _ := newRepos(t)
	ctx := testCtx(t)

	byCity, total, err := repo.List(ctx, domain.ListFilter{City: "Москва", Limit: 50})
	if err != nil {
		t.Fatalf("List by city: %v", err)
	}
	if total != 1 || len(byCity) != 1 || byCity[0].Slug != "pizza-forno" {
		t.Fatalf("city filter returned %+v (total %d)", byCity, total)
	}

	byQuery, _, err := repo.List(ctx, domain.ListFilter{Query: "суши", Limit: 50})
	if err != nil {
		t.Fatalf("List by query: %v", err)
	}
	if len(byQuery) != 1 || byQuery[0].Slug != "sushi-volna" {
		t.Fatalf("case-insensitive search returned %+v", byQuery)
	}

	accepting, _, err := repo.List(ctx, domain.ListFilter{OnlyAccepting: true, Limit: 50})
	if err != nil {
		t.Fatalf("List only accepting: %v", err)
	}
	if len(accepting) != 2 {
		t.Fatalf("only_accepting returned %d merchants, want 2", len(accepting))
	}

	page1, total1, err := repo.List(ctx, domain.ListFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	page2, _, err := repo.List(ctx, domain.ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if total1 != 3 || len(page1) != 2 || len(page2) != 1 {
		t.Fatalf("pagination: total=%d page1=%d page2=%d", total1, len(page1), len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Fatal("pages overlap")
	}

	// A page past the end has no rows, yet the total must still be reported,
	// with the filters applied.
	past, totalPast, err := repo.List(ctx, domain.ListFilter{Limit: 2, Offset: 10})
	if err != nil {
		t.Fatalf("page past the end: %v", err)
	}
	if len(past) != 0 || totalPast != 3 {
		t.Fatalf("past the end: %d merchants, total %d; want 0 and 3", len(past), totalPast)
	}
	_, totalFiltered, err := repo.List(ctx, domain.ListFilter{OnlyAccepting: true, Limit: 2, Offset: 10})
	if err != nil {
		t.Fatalf("filtered page past the end: %v", err)
	}
	if totalFiltered != 2 {
		t.Fatalf("filtered total past the end = %d, want 2", totalFiltered)
	}
}

func TestGetByIDAndByIDs(t *testing.T) {
	_, repo, _ := newRepos(t)
	ctx := testCtx(t)

	m, err := repo.GetByID(ctx, uuid.MustParse(seedKafePrimer))
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if m.Slug != "kafe-primer" || m.MinOrderMinor != 50000 || m.Version != 1 {
		t.Fatalf("merchant = %+v", m)
	}

	// GetByID must expose suspended merchants; only the HTTP layer hides them.
	if _, err := repo.GetByID(ctx, uuid.MustParse(seedSuspended)); err != nil {
		t.Fatalf("GetByID(suspended): %v", err)
	}
	if _, err := repo.GetByID(ctx, uuid.New()); !errors.Is(err, domain.ErrMerchantNotFound) {
		t.Fatalf("GetByID(unknown) = %v, want ErrMerchantNotFound", err)
	}

	items, err := repo.GetByIDs(ctx, []uuid.UUID{uuid.MustParse(seedKafePrimer), uuid.New()})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("GetByIDs returned %d items, want 1", len(items))
	}
	if empty, err := repo.GetByIDs(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("GetByIDs(nil) = %v, %v", empty, err)
	}
}

func TestResolveAPIKey(t *testing.T) {
	_, _, keys := newRepos(t)
	ctx := testCtx(t)

	m, err := keys.ResolveMerchant(ctx, domain.HashAPIKey(devKey))
	if err != nil {
		t.Fatalf("ResolveMerchant: %v", err)
	}
	if m.ID.String() != seedKafePrimer {
		t.Fatalf("merchant id = %s", m.ID)
	}

	if _, err := keys.ResolveMerchant(ctx, domain.HashAPIKey(revokedKey)); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("revoked key = %v, want ErrAPIKeyNotFound", err)
	}
	if _, err := keys.ResolveMerchant(ctx, domain.HashAPIKey("nope-nope-nope-nope")); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("unknown key = %v, want ErrAPIKeyNotFound", err)
	}
}

func TestUpdateProfilePartialAndVersion(t *testing.T) {
	_, repo, _ := newRepos(t)
	ctx := testCtx(t)
	id := uuid.MustParse(seedKafePrimer)

	before, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	updated, err := repo.UpdateProfile(ctx, id,
		domain.ProfilePatch{MinOrderMinor: ptr(int64(70000))}, nil)
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.MinOrderMinor != 70000 {
		t.Fatalf("min_order_minor = %d", updated.MinOrderMinor)
	}
	if updated.DeliveryFeeMinor != before.DeliveryFeeMinor ||
		updated.AvgPrepMinutes != before.AvgPrepMinutes ||
		updated.IsAcceptingOrders != before.IsAcceptingOrders {
		t.Fatalf("absent fields were overwritten: %+v", updated)
	}
	if updated.Version != before.Version+1 {
		t.Fatalf("version = %d, want %d", updated.Version, before.Version+1)
	}
	if !updated.UpdatedAt.After(before.UpdatedAt) && !updated.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("updated_at went backwards")
	}
}

func TestUpdateProfileVersionConflictAndNotFound(t *testing.T) {
	_, repo, _ := newRepos(t)
	ctx := testCtx(t)
	id := uuid.MustParse(seedKafePrimer)

	if _, err := repo.UpdateProfile(ctx, id,
		domain.ProfilePatch{AvgPrepMinutes: ptr(int32(40))}, ptr(int64(999))); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale version = %v, want ErrVersionConflict", err)
	}
	if _, err := repo.UpdateProfile(ctx, uuid.New(),
		domain.ProfilePatch{AvgPrepMinutes: ptr(int32(40))}, nil); !errors.Is(err, domain.ErrMerchantNotFound) {
		t.Fatalf("unknown merchant = %v, want ErrMerchantNotFound", err)
	}
}

// TestUpdateProfileConcurrentConditionalUpdate is the concurrency guarantee of
// this service: several writers holding the same expected version must not all
// succeed. Exactly one conditional UPDATE may match the row.
func TestUpdateProfileConcurrentConditionalUpdate(t *testing.T) {
	_, repo, _ := newRepos(t)
	ctx := testCtx(t)
	id := uuid.MustParse(seedKafePrimer)

	start, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	const writers = 12
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		conflicts int
		other     []error
	)
	begin := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			_, err := repo.UpdateProfile(ctx, id,
				domain.ProfilePatch{AvgPrepMinutes: ptr(int32(10 + i))}, ptr(start.Version))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, domain.ErrVersionConflict):
				conflicts++
			default:
				other = append(other, err)
			}
		}(i)
	}
	close(begin)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if succeeded != 1 || conflicts != writers-1 {
		t.Fatalf("succeeded=%d conflicts=%d, want 1 and %d", succeeded, conflicts, writers-1)
	}
	final, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if final.Version != start.Version+1 {
		t.Fatalf("version = %d, want %d (exactly one write applied)", final.Version, start.Version+1)
	}
}

// TestCheckConstraintsProtectInvariants proves the schema is the last line of
// defence even if a future caller bypasses the domain validation.
func TestCheckConstraintsProtectInvariants(t *testing.T) {
	pool, _, _ := newRepos(t)
	ctx := testCtx(t)

	cases := map[string]string{
		"negative minimum order": `UPDATE merchants SET min_order_minor = -1 WHERE id = '` + seedKafePrimer + `'`,
		"preparation time zero":  `UPDATE merchants SET avg_prep_minutes = 0 WHERE id = '` + seedKafePrimer + `'`,
		"unknown status":         `UPDATE merchants SET status = 'DELETED' WHERE id = '` + seedKafePrimer + `'`,
		"duplicate slug": `INSERT INTO merchants (id, slug, name, city, address, status)
		                   VALUES ('55555555-5555-4555-8555-555555555555','kafe-primer','X','Y','Z','ACTIVE')`,
	}
	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, stmt)
			if err == nil {
				t.Fatal("statement was accepted, want a constraint violation")
			}
			if !pgxx.IsCheckViolation(err) && !pgxx.IsUniqueViolation(err) {
				t.Fatalf("error = %v, want a check or unique violation", err)
			}
		})
	}
}

// TestServiceOverRealStorage exercises the use cases against real SQL, which
// the in-memory doubles cannot validate.
func TestServiceOverRealStorage(t *testing.T) {
	_, repo, keys := newRepos(t)
	ctx := testCtx(t)
	svc := app.NewService(repo, keys, 100)

	m, err := svc.Authenticate(ctx, devKey)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	page, err := svc.ListMerchants(ctx, domain.ListFilter{Limit: 20})
	if err != nil {
		t.Fatalf("ListMerchants: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}
	updated, err := svc.UpdateProfile(ctx, m.ID,
		domain.ProfilePatch{IsAcceptingOrders: ptr(false)}, ptr(m.Version))
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.CanAcceptOrders() {
		t.Fatal("merchant still accepts orders after switching off")
	}
}
