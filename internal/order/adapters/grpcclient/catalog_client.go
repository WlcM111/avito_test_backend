package grpcclient

import (
	"context"
	"time"

	catalogv1 "github.com/avito-kitchen/platform/gen/grpc/catalog/v1"
	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MenuCatalogue quotes names and prices through catalog-service.
type MenuCatalogue struct {
	client  catalogv1.CatalogServiceClient
	timeout time.Duration
}

// NewMenuCatalogue wires the adapter to an existing client connection.
func NewMenuCatalogue(conn grpc.ClientConnInterface, timeout time.Duration) *MenuCatalogue {
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	return &MenuCatalogue{client: catalogv1.NewCatalogServiceClient(conn), timeout: timeout}
}

// QuoteItems resolves positions to their current names and prices.
//
// A position the catalogue cannot sell comes back with a problem code rather
// than as an error, so one call reports the outcome of every requested line.
func (c *MenuCatalogue) QuoteItems(ctx context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) (app.Quote, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	ids := make([]string, 0, len(menuItemIDs))
	for _, id := range menuItemIDs {
		ids = append(ids, id.String())
	}
	resp, err := c.client.QuoteItems(callCtx, &catalogv1.QuoteItemsRequest{
		MerchantId: merchantID.String(), MenuItemIds: ids,
	})
	if err != nil {
		if status.Code(err) == codes.InvalidArgument {
			// The catalogue considers our request malformed. That is our bug,
			// not an outage, and it must not be reported as a retryable 503.
			return app.Quote{}, invalidRequest(depCatalog, err)
		}
		return app.Quote{}, dependencyError(ctx, depCatalog, err)
	}

	out := app.Quote{MenuVersion: resp.GetMenuVersion(),
		Items: make([]app.QuotedItem, 0, len(resp.GetItems()))}
	for _, item := range resp.GetItems() {
		id, parseErr := uuid.Parse(item.GetMenuItemId())
		if parseErr != nil {
			return app.Quote{}, app.Unavailable(depCatalog, parseErr)
		}
		out.Items = append(out.Items, app.QuotedItem{
			MenuItemID:     id,
			ExternalID:     item.GetExternalId(),
			Name:           item.GetName(),
			UnitPriceMinor: item.GetUnitPriceMinor(),
			Problem:        fromProtoItemProblem(item.GetProblem()),
		})
	}
	return out, nil
}

// fromProtoItemProblem maps the catalogue vocabulary onto the order one. An
// unspecified value is treated as "not found": quoting a position we cannot
// classify would risk selling something at an unknown price.
func fromProtoItemProblem(p catalogv1.ItemProblem) domain.ItemProblem {
	switch p {
	case catalogv1.ItemProblem_ITEM_PROBLEM_NONE:
		return domain.ItemProblemNone
	case catalogv1.ItemProblem_ITEM_PROBLEM_NOT_FOUND:
		return domain.ItemProblemNotFound
	case catalogv1.ItemProblem_ITEM_PROBLEM_NOT_IN_MERCHANT:
		return domain.ItemProblemNotInMerchant
	case catalogv1.ItemProblem_ITEM_PROBLEM_UNPUBLISHED:
		return domain.ItemProblemUnpublished
	default:
		return domain.ItemProblemNotFound
	}
}
