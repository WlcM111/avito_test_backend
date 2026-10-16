package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/avito-kitchen/platform/internal/catalog/testsupport"
	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

func newService(t *testing.T) (*app.Service, *testsupport.MenuRepo, *testsupport.MerchantDirectory) {
	t.Helper()
	menus := testsupport.NewMenuRepo()
	dir := testsupport.NewMerchantDirectory()
	return app.NewService(menus, dir, app.Config{MaxQuoteBatch: 5}), menus, dir
}

func TestAuthenticateRejectsAnEmptyKey(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	if _, err := svc.Authenticate(context.Background(), ""); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("Authenticate(\"\") = %v, want ErrMerchantUnauthenticated", err)
	}
}

// A merchant disconnected by the operator keeps a technically valid key but
// must not be able to change its menu.
func TestAuthenticateRejectsASuspendedMerchant(t *testing.T) {
	t.Parallel()
	svc, _, dir := newService(t)
	dir.Keys["suspended"] = app.MerchantIdentity{MerchantID: testsupport.MerchantID, Active: false}
	if _, err := svc.Authenticate(context.Background(), "suspended"); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("Authenticate(suspended) = %v, want ErrMerchantUnauthenticated", err)
	}
}

// An outage of merchant-service must not be reported as a bad key: telling a
// merchant its key is invalid when it is not would be a lie.
func TestAuthenticatePropagatesADirectoryOutage(t *testing.T) {
	t.Parallel()
	svc, _, dir := newService(t)
	dir.Err = app.ErrMerchantDirectoryUnavailable
	_, err := svc.Authenticate(context.Background(), testsupport.DevKey)
	if !errors.Is(err, app.ErrMerchantDirectoryUnavailable) {
		t.Fatalf("Authenticate() = %v, want ErrMerchantDirectoryUnavailable", err)
	}
}

func TestGetPublicMenuReturnsTheSeededMenu(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	menu, err := svc.GetPublicMenu(context.Background(), testsupport.MerchantID)
	if err != nil {
		t.Fatalf("GetPublicMenu() = %v", err)
	}
	if menu.MenuVersion != 1 {
		t.Fatalf("menu version = %d, want 1", menu.MenuVersion)
	}
	if len(menu.Categories) != 2 {
		t.Fatalf("got %d categories, want 2", len(menu.Categories))
	}
	if menu.Categories[0].Category.Name != "Горячее" {
		t.Fatalf("categories are not ordered: %+v", menu.Categories[0].Category)
	}
}

func TestGetPublicMenuOfAnUnknownMerchantIsNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	_, err := svc.GetPublicMenu(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrMenuNotFound) {
		t.Fatalf("GetPublicMenu(unknown) = %v, want ErrMenuNotFound", err)
	}
}

// Before the first publication the merchant must still be able to read its own
// (empty) menu, otherwise it cannot see the state it is about to leave.
func TestGetOwnMenuBeforeTheFirstPublication(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	merchantID := uuid.New()
	menu, err := svc.GetOwnMenu(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("GetOwnMenu() = %v", err)
	}
	if menu.MenuVersion != 0 || len(menu.Categories) != 0 {
		t.Fatalf("empty menu = %+v, want version 0 and no categories", menu)
	}
}

func TestPublishSnapshotWithdrawsAbsentItems(t *testing.T) {
	t.Parallel()
	svc, menus, _ := newService(t)

	result, err := svc.PublishSnapshot(context.Background(), testsupport.MerchantID,
		domain.MenuSnapshot{Categories: []domain.SnapshotCategory{{
			Name: "Горячее",
			Items: []domain.SnapshotItem{
				{ExternalID: testsupport.ExternalSellable, Name: "Борщ", PriceMinor: 43000},
				{ExternalID: "POS-009", Name: "Солянка", PriceMinor: 51000, Position: 1},
			},
		}}}, ptr[int64](1))
	if err != nil {
		t.Fatalf("PublishSnapshot() = %v", err)
	}
	if result.Created != 1 || result.Updated != 1 || result.Withdrawn != 2 {
		t.Fatalf("created=%d updated=%d withdrawn=%d, want 1/1/2",
			result.Created, result.Updated, result.Withdrawn)
	}
	if result.Menu.MenuVersion != 2 {
		t.Fatalf("menu version = %d, want 2", result.Menu.MenuVersion)
	}

	// The withdrawn item keeps its identity: orders and stock cards point at it.
	withdrawn, ok := menus.Item(testsupport.ItemEnabledEmpty)
	if !ok {
		t.Fatal("a withdrawn item must not be deleted physically")
	}
	if !withdrawn.IsDeleted {
		t.Fatal("an item absent from the snapshot must be withdrawn")
	}
	// The republished item keeps its identity and gets the new price.
	kept, ok := menus.Item(testsupport.ItemSellable)
	if !ok || kept.PriceMinor != 43000 {
		t.Fatalf("republished item = %+v, want the new price and the same id", kept)
	}
}

func TestPublishSnapshotRejectsAStaleVersion(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	_, err := svc.PublishSnapshot(context.Background(), testsupport.MerchantID,
		domain.MenuSnapshot{Categories: []domain.SnapshotCategory{{
			Name:  "Горячее",
			Items: []domain.SnapshotItem{{ExternalID: "POS-001", Name: "Борщ", PriceMinor: 1000}},
		}}}, ptr[int64](99))
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("PublishSnapshot(stale) = %v, want ErrVersionConflict", err)
	}
}

// A rejected snapshot must change nothing at all.
func TestPublishSnapshotRejectsAnInvalidMenuWithoutWriting(t *testing.T) {
	t.Parallel()
	svc, menus, _ := newService(t)
	before := menus.Version(testsupport.MerchantID)

	_, err := svc.PublishSnapshot(context.Background(), testsupport.MerchantID,
		domain.MenuSnapshot{Categories: []domain.SnapshotCategory{{
			Name:  "Горячее",
			Items: []domain.SnapshotItem{{ExternalID: "POS-001", Name: "Борщ", PriceMinor: 0}},
		}}}, nil)
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("PublishSnapshot(invalid) = %v, want a validation error", err)
	}
	if after := menus.Version(testsupport.MerchantID); after != before {
		t.Fatalf("menu version changed from %d to %d on a rejected snapshot", before, after)
	}
}

func TestUpdateMenuItemBumpsTheMenuVersion(t *testing.T) {
	t.Parallel()
	svc, menus, _ := newService(t)
	before := menus.Version(testsupport.MerchantID)

	result, err := svc.UpdateMenuItem(context.Background(), testsupport.MerchantID,
		testsupport.ItemSellable, domain.ItemPatch{PriceMinor: ptr[int64](44000)}, ptr(before))
	if err != nil {
		t.Fatalf("UpdateMenuItem() = %v", err)
	}
	if result.Item.PriceMinor != 44000 {
		t.Fatalf("price = %d, want 44000", result.Item.PriceMinor)
	}
	if result.MenuVersion != before+1 {
		t.Fatalf("menu version = %d, want %d", result.MenuVersion, before+1)
	}
}

// The name of a patched item is trimmed like a published one, and a name of
// spaces only is refused instead of being stored.
func TestUpdateMenuItemTrimsTheName(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)

	result, err := svc.UpdateMenuItem(context.Background(), testsupport.MerchantID,
		testsupport.ItemSellable, domain.ItemPatch{Name: ptr("  Борщ  ")}, nil)
	if err != nil {
		t.Fatalf("UpdateMenuItem() = %v", err)
	}
	if result.Item.Name != "Борщ" {
		t.Fatalf("name = %q, want it trimmed", result.Item.Name)
	}

	_, err = svc.UpdateMenuItem(context.Background(), testsupport.MerchantID,
		testsupport.ItemSellable, domain.ItemPatch{Name: ptr("   ")}, nil)
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("UpdateMenuItem(blank name) = %v, want a validation error", err)
	}
}

func TestUpdateMenuItemRejectsAnEmptyPatch(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	_, err := svc.UpdateMenuItem(context.Background(), testsupport.MerchantID,
		testsupport.ItemSellable, domain.ItemPatch{}, nil)
	if _, ok := domain.AsValidationError(err); !ok {
		t.Fatalf("UpdateMenuItem(empty) = %v, want a validation error", err)
	}
}

func TestUpdateMenuItemOfAnotherMerchantIsNotFound(t *testing.T) {
	t.Parallel()
	svc, menus, _ := newService(t)
	foreign := uuid.New()
	menus.AddForeignItem(foreign)

	_, err := svc.UpdateMenuItem(context.Background(), testsupport.MerchantID, foreign,
		domain.ItemPatch{PriceMinor: ptr[int64](1000)}, nil)
	if !errors.Is(err, domain.ErrMenuItemNotFound) {
		t.Fatalf("UpdateMenuItem(foreign) = %v, want ErrMenuItemNotFound", err)
	}
}

func TestQuoteItemsReportsEveryProblem(t *testing.T) {
	t.Parallel()
	svc, menus, _ := newService(t)
	foreign := uuid.New()
	menus.AddForeignItem(foreign)
	unknown := uuid.New()

	// Withdraw one item by republishing the menu without it.
	if _, err := svc.PublishSnapshot(context.Background(), testsupport.MerchantID,
		domain.MenuSnapshot{Categories: []domain.SnapshotCategory{{
			Name: "Горячее",
			Items: []domain.SnapshotItem{
				{ExternalID: testsupport.ExternalSellable, Name: "Борщ", PriceMinor: 42000},
			},
		}}}, nil); err != nil {
		t.Fatalf("PublishSnapshot() = %v", err)
	}

	result, err := svc.QuoteItems(context.Background(), testsupport.MerchantID,
		[]uuid.UUID{testsupport.ItemSellable, testsupport.ItemEnabledEmpty, foreign, unknown})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	if len(result.Items) != 4 {
		t.Fatalf("got %d quotes, want 4", len(result.Items))
	}
	want := []domain.ItemProblem{
		domain.ItemProblemNone,
		domain.ItemProblemUnpublished,
		domain.ItemProblemNotInMerchant,
		domain.ItemProblemNotFound,
	}
	for i, problem := range want {
		if result.Items[i].Problem != problem {
			t.Fatalf("quote[%d].Problem = %s, want %s", i, result.Items[i].Problem, problem)
		}
	}
	if result.Items[0].UnitPriceMinor != 42000 || result.Items[0].ExternalID != testsupport.ExternalSellable {
		t.Fatalf("quoted item = %+v", result.Items[0])
	}
	// A problem line carries no price: the caller must not charge for it.
	if result.Items[1].UnitPriceMinor != 0 {
		t.Fatalf("a withdrawn item must not carry a price: %+v", result.Items[1])
	}
}

func TestQuoteItemsValidatesTheBatch(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)

	if _, err := svc.QuoteItems(context.Background(), uuid.Nil, []uuid.UUID{uuid.New()}); err == nil {
		t.Fatal("QuoteItems without a merchant must be rejected")
	}
	if _, err := svc.QuoteItems(context.Background(), testsupport.MerchantID, nil); err == nil {
		t.Fatal("QuoteItems without identifiers must be rejected")
	}
	tooMany := make([]uuid.UUID, 0, 6)
	for i := 0; i < 6; i++ {
		tooMany = append(tooMany, uuid.New())
	}
	if _, err := svc.QuoteItems(context.Background(), testsupport.MerchantID, tooMany); err == nil {
		t.Fatal("an oversized batch must be rejected")
	}
}

// Repeated identifiers must not multiply the answer.
func TestQuoteItemsDeduplicates(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	result, err := svc.QuoteItems(context.Background(), testsupport.MerchantID,
		[]uuid.UUID{testsupport.ItemSellable, testsupport.ItemSellable})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("got %d quotes, want 1", len(result.Items))
	}
}
