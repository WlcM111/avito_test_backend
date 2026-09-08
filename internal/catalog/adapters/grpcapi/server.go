// Package grpcapi is the inbound gRPC adapter of catalog-service. It serves
// catalog.v1.CatalogService for order-service and translates domain errors into
// gRPC status codes. It contains no business rules.
package grpcapi

import (
	"context"
	"errors"

	catalogv1 "github.com/avito-kitchen/platform/gen/grpc/catalog/v1"
	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServiceName is the fully qualified gRPC service name, used for health
// reporting and for client configuration in order-service.
const ServiceName = "catalog.v1.CatalogService"

// Server implements catalog.v1.CatalogService.
type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
	svc *app.Service
}

// NewServer wires the gRPC adapter to the use cases.
func NewServer(svc *app.Service) *Server { return &Server{svc: svc} }

// Register adds the service to a gRPC registrar.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	catalogv1.RegisterCatalogServiceServer(reg, s)
}

// QuoteItems resolves menu item ids to their current names and prices.
//
// Items that do not exist, are withdrawn or belong to another merchant are
// returned with the corresponding problem code instead of failing the call:
// order-service needs the outcome of every requested line at once.
func (s *Server) QuoteItems(ctx context.Context,
	req *catalogv1.QuoteItemsRequest) (*catalogv1.QuoteItemsResponse, error) {
	merchantID, err := uuid.Parse(req.GetMerchantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "merchant_id must be a UUID")
	}
	ids, err := parseUUIDs(req.GetMenuItemIds(), "menu_item_ids")
	if err != nil {
		return nil, err
	}

	result, err := s.svc.QuoteItems(ctx, merchantID, ids)
	if err != nil {
		return nil, toStatusError(err)
	}

	items := make([]*catalogv1.QuotedItem, 0, len(result.Items))
	for _, q := range result.Items {
		items = append(items, &catalogv1.QuotedItem{
			MenuItemId:     q.MenuItemID.String(),
			ExternalId:     q.ExternalID,
			Name:           q.Name,
			UnitPriceMinor: q.UnitPriceMinor,
			Problem:        toProtoItemProblem(q.Problem),
		})
	}
	return &catalogv1.QuoteItemsResponse{
		MenuVersion: result.MenuVersion,
		Items:       items,
	}, nil
}

func parseUUIDs(raw []string, field string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(raw))
	for _, v := range raw {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%s contains a non-UUID value", field)
		}
		out = append(out, id)
	}
	return out, nil
}

func toProtoItemProblem(p domain.ItemProblem) catalogv1.ItemProblem {
	switch p {
	case domain.ItemProblemNone:
		return catalogv1.ItemProblem_ITEM_PROBLEM_NONE
	case domain.ItemProblemNotFound:
		return catalogv1.ItemProblem_ITEM_PROBLEM_NOT_FOUND
	case domain.ItemProblemNotInMerchant:
		return catalogv1.ItemProblem_ITEM_PROBLEM_NOT_IN_MERCHANT
	case domain.ItemProblemUnpublished:
		return catalogv1.ItemProblem_ITEM_PROBLEM_UNPUBLISHED
	default:
		return catalogv1.ItemProblem_ITEM_PROBLEM_UNSPECIFIED
	}
}

// toStatusError maps domain and application errors onto gRPC codes. Anything
// unmapped becomes Internal with a generic message so storage details never
// leak to the caller.
func toStatusError(err error) error {
	switch {
	case errors.Is(err, domain.ErrMenuNotFound):
		return status.Error(codes.NotFound, "menu not found")
	case errors.Is(err, domain.ErrMenuItemNotFound):
		return status.Error(codes.NotFound, "menu item not found")
	case errors.Is(err, domain.ErrVersionConflict):
		return status.Error(codes.Aborted, "menu version conflict")
	case errors.Is(err, app.ErrMerchantUnauthenticated):
		return status.Error(codes.Unauthenticated, "merchant is not authenticated")
	case errors.Is(err, app.ErrMerchantDirectoryUnavailable):
		return status.Error(codes.Unavailable, "merchant directory is unavailable")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}
	if ve, ok := domain.AsValidationError(err); ok {
		return status.Error(codes.InvalidArgument, ve.Error())
	}
	return status.Error(codes.Internal, "internal error")
}
