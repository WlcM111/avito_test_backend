package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pgstore "github.com/avito-kitchen/platform/internal/catalog/adapters/postgres"
	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/avito-kitchen/platform/test/testenv"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixed identifiers of migrations/catalog/0002_seed.sql. They are the same
// menu item ids that migrations/inventory/0002_seed.sql keeps stock cards for.
var (
	merchantID       = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	itemSellable     = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	itemEnabledEmpty = uuid.MustParse("a1111111-1111-4111-8111-222222222222")
	itemStopped      = uuid.MustParse("a1111111-1111-4111-8111-333333333333")
)

const (
	externalSellable     = "POS-001"
	externalEnabledEmpty = "POS-002"
	externalStopped      = "POS-003"
)

func ptr[T any](v T) *T { return &v }

type env struct {
	pool *pgxpool.Pool
	repo *pgstore.MenuRepository
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testenv.RequireDB(t, migrations.DirCatalog)
	return env{pool: pool, repo: pgstore.NewMenuRepository(pgxx.NewTxManager(pool))}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func snapshot(merchant uuid.UUID, categories ...domain.SnapshotCategory) domain.MenuSnapshot {
	return domain.MenuSnapshot{MerchantID: merchant, Categories: categories}.Normalize()
}

func TestMigrationsAreIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	before, err := testenv.MigrationVersion(ctx, e.pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := testenv.ApplyAgain(ctx, e.pool, migrations.DirCatalog); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after, err := testenv.MigrationVersion(ctx, e.pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if before != after {
		t.Fatalf("migration version changed from %d to %d on a repeated apply", before, after)
	}
}

func TestGetMenuReturnsTheSeededMenu(t *testing.T) {
	e := newEnv(t)
	menu, err := e.repo.GetMenu(testCtx(t), merchantID)
	if err != nil {
		t.Fatalf("GetMenu() = %v", err)
	}
	if menu.MenuVersion != 1 {
		t.Fatalf("menu version = %d, want 1", menu.MenuVersion)
	}
	if len(menu.Categories) != 2 {
		t.Fatalf("got %d categories, want 2", len(menu.Categories))
	}
	if menu.Categories[0].Category.Name != "Горячее" || len(menu.Categories[0].Items) != 2 {
		t.Fatalf("first category = %+v", menu.Categories[0])
	}
	if menu.Categories[0].Items[0].ID != itemSellable {
		t.Fatalf("items are not in display order: %+v", menu.Categories[0].Items)
	}
}

func TestGetMenuOfAnUnpublishedMerchantIsNotFound(t *testing.T) {
	e := newEnv(t)
	if _, err := e.repo.GetMenu(testCtx(t), uuid.New()); !errors.Is(err, domain.ErrMenuNotFound) {
		t.Fatalf("GetMenu(unknown) = %v, want ErrMenuNotFound", err)
	}
}

func TestFirstPublicationCreatesVersionOne(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)
	fresh := uuid.New()

	result, err := e.repo.PublishSnapshot(ctx, snapshot(fresh, domain.SnapshotCategory{
		Name:  "Пицца",
		Items: []domain.SnapshotItem{{ExternalID: "P-1", Name: "Маргарита", PriceMinor: 55000}},
	}), ptr[int64](0))
	if err != nil {
		t.Fatalf("PublishSnapshot() = %v", err)
	}
	if result.Menu.MenuVersion != 1 {
		t.Fatalf("menu version = %d, want 1", result.Menu.MenuVersion)
	}
	if result.Created != 1 || result.Updated != 0 || result.Withdrawn != 0 {
		t.Fatalf("created=%d updated=%d withdrawn=%d, want 1/0/0",
			result.Created, result.Updated, result.Withdrawn)
	}
}

// Republishing must keep the platform identifier of an item that is still in
// the menu: stock cards and order snapshots point at it.
func TestRepublishKeepsIdentityAndWithdrawsAbsentItems(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	result, err := e.repo.PublishSnapshot(ctx, snapshot(merchantID, domain.SnapshotCategory{
		Name: "Горячее",
		Items: []domain.SnapshotItem{
			{ExternalID: externalSellable, Name: "Борщ", PriceMinor: 43000},
			{ExternalID: "POS-009", Name: "Солянка", PriceMinor: 51000, Position: 1},
		},
	}), ptr[int64](1))
	if err != nil {
		t.Fatalf("PublishSnapshot() = %v", err)
	}
	if result.Menu.MenuVersion != 2 {
		t.Fatalf("menu version = %d, want 2", result.Menu.MenuVersion)
	}
	if result.Created != 1 || result.Updated != 1 || result.Withdrawn != 2 {
		t.Fatalf("created=%d updated=%d withdrawn=%d, want 1/1/2",
			result.Created, result.Updated, result.Withdrawn)
	}

	var (
		id      uuid.UUID
		price   int64
		deleted bool
	)
	if err := e.pool.QueryRow(ctx,
		`SELECT id, price_minor, is_deleted FROM menu_items
		 WHERE merchant_id = $1 AND external_id = $2`,
		merchantID, externalSellable).Scan(&id, &price, &deleted); err != nil {
		t.Fatalf("read republished item: %v", err)
	}
	if id != itemSellable {
		t.Fatalf("republished item id = %s, want the stable %s", id, itemSellable)
	}
	if price != 43000 || deleted {
		t.Fatalf("republished item price=%d deleted=%v", price, deleted)
	}

	// The withdrawn items are still there, only flagged.
	var withdrawn int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM menu_items WHERE merchant_id = $1 AND is_deleted`,
		merchantID).Scan(&withdrawn); err != nil {
		t.Fatalf("count withdrawn: %v", err)
	}
	if withdrawn != 2 {
		t.Fatalf("withdrawn rows = %d, want 2", withdrawn)
	}
	for _, gone := range []uuid.UUID{itemEnabledEmpty, itemStopped} {
		var exists bool
		if err := e.pool.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM menu_items WHERE id = $1)`, gone).Scan(&exists); err != nil {
			t.Fatalf("check row %s: %v", gone, err)
		}
		if !exists {
			t.Fatalf("item %s was deleted physically", gone)
		}
	}

	// The read path no longer shows them.
	menu, err := e.repo.GetMenu(ctx, merchantID)
	if err != nil {
		t.Fatalf("GetMenu() = %v", err)
	}
	if len(menu.Categories) != 1 || len(menu.Categories[0].Items) != 2 {
		t.Fatalf("menu after republish = %+v", menu.Categories)
	}
}

// Bringing a withdrawn item back must reuse its identifier rather than create
// a second row for the same position.
func TestWithdrawnItemIsRestoredWithTheSameIdentifier(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	if _, err := e.repo.PublishSnapshot(ctx, snapshot(merchantID, domain.SnapshotCategory{
		Name:  "Горячее",
		Items: []domain.SnapshotItem{{ExternalID: externalSellable, Name: "Борщ", PriceMinor: 42000}},
	}), nil); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := e.repo.PublishSnapshot(ctx, snapshot(merchantID, domain.SnapshotCategory{
		Name: "Горячее",
		Items: []domain.SnapshotItem{
			{ExternalID: externalSellable, Name: "Борщ", PriceMinor: 42000},
			{ExternalID: externalEnabledEmpty, Name: "Котлета", PriceMinor: 56000, Position: 1},
		},
	}), nil); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	var (
		id      uuid.UUID
		deleted bool
	)
	if err := e.pool.QueryRow(ctx,
		`SELECT id, is_deleted FROM menu_items WHERE merchant_id = $1 AND external_id = $2`,
		merchantID, externalEnabledEmpty).Scan(&id, &deleted); err != nil {
		t.Fatalf("read restored item: %v", err)
	}
	if id != itemEnabledEmpty || deleted {
		t.Fatalf("restored item id=%s deleted=%v, want %s and false", id, deleted, itemEnabledEmpty)
	}
}

func TestPublishWithAStaleVersionIsRejected(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	_, err := e.repo.PublishSnapshot(ctx, snapshot(merchantID, domain.SnapshotCategory{
		Name:  "Горячее",
		Items: []domain.SnapshotItem{{ExternalID: externalSellable, Name: "Борщ", PriceMinor: 1000}},
	}), ptr[int64](99))
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("PublishSnapshot(stale) = %v, want ErrVersionConflict", err)
	}

	// Nothing may have changed.
	menu, err := e.repo.GetMenu(ctx, merchantID)
	if err != nil {
		t.Fatalf("GetMenu() = %v", err)
	}
	if menu.MenuVersion != 1 {
		t.Fatalf("menu version = %d after a rejected publication, want 1", menu.MenuVersion)
	}
}

// Two terminals publishing the same version concurrently: the row lock on
// merchant_menu_state lets exactly one through, the other sees the conflict
// instead of silently overwriting the first.
func TestConcurrentPublishAllowsExactlyOne(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	const attempts = 4
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		conflicts int
		other     []error
	)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := e.repo.PublishSnapshot(ctx, snapshot(merchantID, domain.SnapshotCategory{
				Name: "Горячее",
				Items: []domain.SnapshotItem{
					{ExternalID: externalSellable, Name: "Борщ", PriceMinor: int64(40000 + n)},
				},
			}), ptr[int64](1))
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
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if succeeded != 1 || conflicts != attempts-1 {
		t.Fatalf("succeeded=%d conflicts=%d, want 1 and %d", succeeded, conflicts, attempts-1)
	}
	menu, err := e.repo.GetMenu(ctx, merchantID)
	if err != nil {
		t.Fatalf("GetMenu() = %v", err)
	}
	if menu.MenuVersion != 2 {
		t.Fatalf("menu version = %d after %d concurrent publications, want 2",
			menu.MenuVersion, attempts)
	}
}

func TestUpdateItemChangesThePriceAndBumpsTheVersion(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	item, version, err := e.repo.UpdateItem(ctx, merchantID, itemSellable,
		domain.ItemPatch{PriceMinor: ptr[int64](44500)}, ptr[int64](1))
	if err != nil {
		t.Fatalf("UpdateItem() = %v", err)
	}
	if item.PriceMinor != 44500 || item.Name != "Борщ с говядиной" {
		t.Fatalf("updated item = %+v, want only the price changed", item)
	}
	if version != 2 {
		t.Fatalf("menu version = %d, want 2", version)
	}
}

func TestUpdateItemRejectsAStaleVersionAndUnknownItems(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	if _, _, err := e.repo.UpdateItem(ctx, merchantID, itemSellable,
		domain.ItemPatch{PriceMinor: ptr[int64](1000)}, ptr[int64](42)); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("UpdateItem(stale) = %v, want ErrVersionConflict", err)
	}
	if _, _, err := e.repo.UpdateItem(ctx, merchantID, uuid.New(),
		domain.ItemPatch{PriceMinor: ptr[int64](1000)}, nil); !errors.Is(err, domain.ErrMenuItemNotFound) {
		t.Fatalf("UpdateItem(unknown) = %v, want ErrMenuItemNotFound", err)
	}
	// An item of another merchant is not reachable either.
	fresh := uuid.New()
	if _, _, err := e.repo.UpdateItem(ctx, fresh, itemSellable,
		domain.ItemPatch{PriceMinor: ptr[int64](1000)}, nil); !errors.Is(err, domain.ErrMenuItemNotFound) {
		t.Fatalf("UpdateItem(foreign) = %v, want ErrMenuItemNotFound", err)
	}
}

func TestUpdateItemDoesNotResurrectAWithdrawnItem(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	if _, err := e.repo.PublishSnapshot(ctx, snapshot(merchantID, domain.SnapshotCategory{
		Name:  "Горячее",
		Items: []domain.SnapshotItem{{ExternalID: externalSellable, Name: "Борщ", PriceMinor: 42000}},
	}), nil); err != nil {
		t.Fatalf("PublishSnapshot() = %v", err)
	}
	if _, _, err := e.repo.UpdateItem(ctx, merchantID, itemStopped,
		domain.ItemPatch{PriceMinor: ptr[int64](1000)}, nil); !errors.Is(err, domain.ErrMenuItemNotFound) {
		t.Fatalf("UpdateItem(withdrawn) = %v, want ErrMenuItemNotFound", err)
	}
}

func TestQuoteItemsClassifiesEveryLine(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	// A menu of a second merchant provides a genuinely foreign item.
	foreign := uuid.New()
	if _, err := e.repo.PublishSnapshot(ctx, snapshot(foreign, domain.SnapshotCategory{
		Name:  "Роллы",
		Items: []domain.SnapshotItem{{ExternalID: "OTHER-1", Name: "Филадельфия", PriceMinor: 99000}},
	}), nil); err != nil {
		t.Fatalf("publish foreign menu: %v", err)
	}
	var foreignItemID uuid.UUID
	if err := e.pool.QueryRow(ctx,
		`SELECT id FROM menu_items WHERE merchant_id = $1`, foreign).Scan(&foreignItemID); err != nil {
		t.Fatalf("read foreign item: %v", err)
	}

	// Withdraw one item of the demonstration merchant.
	if _, err := e.repo.PublishSnapshot(ctx, snapshot(merchantID, domain.SnapshotCategory{
		Name: "Горячее",
		Items: []domain.SnapshotItem{
			{ExternalID: externalSellable, Name: "Борщ", PriceMinor: 42000},
		},
	}), nil); err != nil {
		t.Fatalf("republish: %v", err)
	}

	unknown := uuid.New()
	version, quotes, err := e.repo.QuoteItems(ctx, merchantID,
		[]uuid.UUID{itemSellable, itemEnabledEmpty, foreignItemID, unknown})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	if version != 2 {
		t.Fatalf("menu version = %d, want 2", version)
	}
	want := []domain.ItemProblem{
		domain.ItemProblemNone,
		domain.ItemProblemUnpublished,
		domain.ItemProblemNotInMerchant,
		domain.ItemProblemNotFound,
	}
	for i, problem := range want {
		if quotes[i].Problem != problem {
			t.Fatalf("quote[%d] = %+v, want problem %s", i, quotes[i], problem)
		}
	}
	if quotes[0].UnitPriceMinor != 42000 || quotes[0].ExternalID != externalSellable {
		t.Fatalf("quoted item = %+v", quotes[0])
	}
	if quotes[1].UnitPriceMinor != 0 || quotes[2].UnitPriceMinor != 0 {
		t.Fatalf("a problem line must not carry a price: %+v %+v", quotes[1], quotes[2])
	}
}

func TestQuoteItemsForAMerchantWithoutAMenu(t *testing.T) {
	e := newEnv(t)
	version, quotes, err := e.repo.QuoteItems(testCtx(t), uuid.New(), []uuid.UUID{itemSellable})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	if version != 0 {
		t.Fatalf("menu version = %d, want 0", version)
	}
	if quotes[0].Problem != domain.ItemProblemNotInMerchant {
		t.Fatalf("problem = %s, want NOT_IN_MERCHANT", quotes[0].Problem)
	}
}

// The schema is the last line of defence: even if a caller bypassed the domain
// rules, the database refuses a non-positive price and a duplicate external id.
func TestSchemaConstraints(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	var categoryID uuid.UUID
	if err := e.pool.QueryRow(ctx,
		`SELECT id FROM menu_categories WHERE merchant_id = $1 ORDER BY position LIMIT 1`,
		merchantID).Scan(&categoryID); err != nil {
		t.Fatalf("read category: %v", err)
	}

	_, err := e.pool.Exec(ctx,
		`INSERT INTO menu_items (id, merchant_id, category_id, external_id, name, price_minor)
		 VALUES ($1, $2, $3, 'POS-BAD', 'Бесплатно', 0)`,
		uuid.New(), merchantID, categoryID)
	if !pgxx.IsCheckViolation(err) {
		t.Fatalf("insert with a zero price = %v, want a CHECK violation", err)
	}

	_, err = e.pool.Exec(ctx,
		`INSERT INTO menu_items (id, merchant_id, category_id, external_id, name, price_minor)
		 VALUES ($1, $2, $3, $4, 'Дубль', 1000)`,
		uuid.New(), merchantID, categoryID, externalStopped)
	if !pgxx.IsUniqueViolation(err) {
		t.Fatalf("duplicate external id = %v, want a UNIQUE violation", err)
	}

	_, err = e.pool.Exec(ctx,
		`INSERT INTO merchant_menu_state (merchant_id, menu_version) VALUES ($1, 0)`, uuid.New())
	if !pgxx.IsCheckViolation(err) {
		t.Fatalf("menu version 0 = %v, want a CHECK violation", err)
	}
}

// A published menu never carries availability: that dimension belongs to
// inventory-service, and the catalogue schema has no column for it.
func TestCatalogueOwnsNoQuantity(t *testing.T) {
	e := newEnv(t)
	var columns int
	if err := e.pool.QueryRow(testCtx(t),
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'menu_items'
		   AND column_name IN ('qty', 'quantity', 'on_hand_qty', 'reserved_qty',
		                       'stock', 'available_qty', 'sales_state')`).Scan(&columns); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if columns != 0 {
		t.Fatalf("menu_items carries %d quantity columns, want none", columns)
	}
}

var _ app.MenuRepository = (*pgstore.MenuRepository)(nil)
