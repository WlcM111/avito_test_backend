package grpcapi_test

import (
	"context"
	"net"
	"testing"
	"time"

	catalogv1 "github.com/avito-kitchen/platform/gen/grpc/catalog/v1"
	"github.com/avito-kitchen/platform/internal/catalog/adapters/grpcapi"
	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/avito-kitchen/platform/internal/catalog/testsupport"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newClient starts the real gRPC server over an in-memory listener, so the
// frozen contract is exercised through actual encoding and status propagation
// rather than through a mocked client.
func newClient(t *testing.T) (catalogv1.CatalogServiceClient, *testsupport.MenuRepo) {
	t.Helper()
	menus := testsupport.NewMenuRepo()
	svc := app.NewService(menus, testsupport.NewMerchantDirectory(), app.Config{MaxQuoteBatch: 5})

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	grpcapi.NewServer(svc).Register(srv)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return catalogv1.NewCatalogServiceClient(conn), menus
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestQuoteItemsReturnsNamesAndPrices(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)

	resp, err := client.QuoteItems(ctxWithTimeout(t), &catalogv1.QuoteItemsRequest{
		MerchantId: testsupport.MerchantID.String(),
		MenuItemIds: []string{
			testsupport.ItemSellable.String(),
			testsupport.ItemStopped.String(),
		},
	})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	if resp.GetMenuVersion() != 1 {
		t.Fatalf("menu version = %d, want 1", resp.GetMenuVersion())
	}
	if len(resp.GetItems()) != 2 {
		t.Fatalf("got %d items, want 2", len(resp.GetItems()))
	}
	first := resp.GetItems()[0]
	if first.GetProblem() != catalogv1.ItemProblem_ITEM_PROBLEM_NONE {
		t.Fatalf("problem = %s, want NONE", first.GetProblem())
	}
	if first.GetUnitPriceMinor() != 42000 || first.GetExternalId() != testsupport.ExternalSellable {
		t.Fatalf("quoted item = %+v", first)
	}
	// The stop list belongs to inventory-service: the catalogue quotes the
	// item regardless of whether it can currently be sold.
	if resp.GetItems()[1].GetProblem() != catalogv1.ItemProblem_ITEM_PROBLEM_NONE {
		t.Fatalf("a stop-listed item must still be quoted: %+v", resp.GetItems()[1])
	}
}

// Problems travel in the response, not as an error: order-service needs the
// outcome of every requested line at once.
func TestQuoteItemsReportsProblemsWithoutFailing(t *testing.T) {
	t.Parallel()
	client, menus := newClient(t)
	foreign := uuid.New()
	menus.AddForeignItem(foreign)
	unknown := uuid.New()

	resp, err := client.QuoteItems(ctxWithTimeout(t), &catalogv1.QuoteItemsRequest{
		MerchantId:  testsupport.MerchantID.String(),
		MenuItemIds: []string{unknown.String(), foreign.String()},
	})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	byID := map[string]catalogv1.ItemProblem{}
	for _, item := range resp.GetItems() {
		byID[item.GetMenuItemId()] = item.GetProblem()
	}
	if byID[unknown.String()] != catalogv1.ItemProblem_ITEM_PROBLEM_NOT_FOUND {
		t.Fatalf("unknown item problem = %s", byID[unknown.String()])
	}
	if byID[foreign.String()] != catalogv1.ItemProblem_ITEM_PROBLEM_NOT_IN_MERCHANT {
		t.Fatalf("foreign item problem = %s", byID[foreign.String()])
	}
}

func TestQuoteItemsReportsWithdrawnItems(t *testing.T) {
	t.Parallel()
	client, menus := newClient(t)

	// Republish the menu without POS-002; the item stays addressable.
	if _, err := menus.PublishSnapshot(context.Background(), domain.MenuSnapshot{
		MerchantID: testsupport.MerchantID,
		Categories: []domain.SnapshotCategory{{
			Name: "Горячее",
			Items: []domain.SnapshotItem{
				{ExternalID: testsupport.ExternalSellable, Name: "Борщ", PriceMinor: 42000},
			},
		}},
	}, nil); err != nil {
		t.Fatalf("PublishSnapshot() = %v", err)
	}

	resp, err := client.QuoteItems(ctxWithTimeout(t), &catalogv1.QuoteItemsRequest{
		MerchantId:  testsupport.MerchantID.String(),
		MenuItemIds: []string{testsupport.ItemEnabledEmpty.String()},
	})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	item := resp.GetItems()[0]
	if item.GetProblem() != catalogv1.ItemProblem_ITEM_PROBLEM_UNPUBLISHED {
		t.Fatalf("problem = %s, want UNPUBLISHED", item.GetProblem())
	}
	if item.GetUnitPriceMinor() != 0 {
		t.Fatalf("a withdrawn item must not carry a price: %+v", item)
	}
	if resp.GetMenuVersion() != 2 {
		t.Fatalf("menu version = %d, want 2", resp.GetMenuVersion())
	}
}

func TestQuoteItemsRejectsBadArguments(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)
	ctx := ctxWithTimeout(t)

	cases := []struct {
		name string
		req  *catalogv1.QuoteItemsRequest
	}{
		{
			name: "merchant is not a uuid",
			req:  &catalogv1.QuoteItemsRequest{MerchantId: "nope", MenuItemIds: []string{uuid.NewString()}},
		},
		{
			name: "item is not a uuid",
			req: &catalogv1.QuoteItemsRequest{
				MerchantId: testsupport.MerchantID.String(), MenuItemIds: []string{"nope"},
			},
		},
		{
			name: "no identifiers",
			req:  &catalogv1.QuoteItemsRequest{MerchantId: testsupport.MerchantID.String()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.QuoteItems(ctx, tc.req); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("QuoteItems() = %v, want INVALID_ARGUMENT", err)
			}
		})
	}
}

func TestQuoteItemsRejectsAnOversizedBatch(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)
	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		ids = append(ids, uuid.NewString())
	}
	_, err := client.QuoteItems(ctxWithTimeout(t), &catalogv1.QuoteItemsRequest{
		MerchantId: testsupport.MerchantID.String(), MenuItemIds: ids,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("QuoteItems() = %v, want INVALID_ARGUMENT", err)
	}
}

// A merchant that never published is not an error for the quote path: every
// requested line is simply unknown, and the version is zero.
func TestQuoteItemsForAMerchantWithoutAMenu(t *testing.T) {
	t.Parallel()
	client, _ := newClient(t)
	resp, err := client.QuoteItems(ctxWithTimeout(t), &catalogv1.QuoteItemsRequest{
		MerchantId:  uuid.NewString(),
		MenuItemIds: []string{uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	if resp.GetMenuVersion() != 0 {
		t.Fatalf("menu version = %d, want 0", resp.GetMenuVersion())
	}
	if resp.GetItems()[0].GetProblem() != catalogv1.ItemProblem_ITEM_PROBLEM_NOT_FOUND {
		t.Fatalf("problem = %s, want NOT_FOUND", resp.GetItems()[0].GetProblem())
	}
}
