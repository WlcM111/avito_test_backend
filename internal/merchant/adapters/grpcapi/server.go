// Package grpcapi is the inbound gRPC adapter of merchant-service. It serves
// merchant.v1.MerchantService for the other platform services and translates
// domain errors into gRPC status codes. It contains no business rules.
package grpcapi

import (
	"context"
	"errors"

	merchantv1 "github.com/avito-kitchen/platform/gen/grpc/merchant/v1"
	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServiceName is the fully qualified gRPC service name, used for health
// reporting and for client configuration in other services.
const ServiceName = "merchant.v1.MerchantService"

// Server implements merchant.v1.MerchantService.
type Server struct {
	merchantv1.UnimplementedMerchantServiceServer
	svc *app.Service
}

// NewServer wires the gRPC adapter to the use cases.
func NewServer(svc *app.Service) *Server { return &Server{svc: svc} }

// Register adds the service to a gRPC registrar.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	merchantv1.RegisterMerchantServiceServer(reg, s)
}

// AuthenticateApiKey resolves an access key to its merchant.
func (s *Server) AuthenticateApiKey(ctx context.Context,
	req *merchantv1.AuthenticateApiKeyRequest) (*merchantv1.AuthenticateApiKeyResponse, error) {
	if req.GetApiKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_key is required")
	}
	m, err := s.svc.Authenticate(ctx, req.GetApiKey())
	if err != nil {
		return nil, toStatusError(err)
	}
	return &merchantv1.AuthenticateApiKeyResponse{
		MerchantId: m.ID.String(),
		Status:     toProtoStatus(m.Status),
	}, nil
}

// GetMerchant returns the commercial profile of one merchant, whatever its
// status: internal callers need to see suspended merchants too.
func (s *Server) GetMerchant(ctx context.Context,
	req *merchantv1.GetMerchantRequest) (*merchantv1.GetMerchantResponse, error) {
	id, err := uuid.Parse(req.GetMerchantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "merchant_id must be a UUID")
	}
	m, err := s.svc.GetMerchant(ctx, id)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &merchantv1.GetMerchantResponse{Merchant: toProtoMerchant(m)}, nil
}

// GetMerchants returns the merchants that exist among the requested ids.
func (s *Server) GetMerchants(ctx context.Context,
	req *merchantv1.GetMerchantsRequest) (*merchantv1.GetMerchantsResponse, error) {
	ids := make([]uuid.UUID, 0, len(req.GetMerchantIds()))
	for _, raw := range req.GetMerchantIds() {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "merchant_ids contains a non-UUID value")
		}
		ids = append(ids, id)
	}
	items, err := s.svc.GetMerchants(ctx, ids)
	if err != nil {
		return nil, toStatusError(err)
	}
	out := make([]*merchantv1.Merchant, 0, len(items))
	for _, m := range items {
		out = append(out, toProtoMerchant(m))
	}
	return &merchantv1.GetMerchantsResponse{Merchants: out}, nil
}

func toProtoStatus(s domain.Status) merchantv1.MerchantStatus {
	switch s {
	case domain.StatusActive:
		return merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE
	case domain.StatusSuspended:
		return merchantv1.MerchantStatus_MERCHANT_STATUS_SUSPENDED
	default:
		return merchantv1.MerchantStatus_MERCHANT_STATUS_UNSPECIFIED
	}
}

func toProtoMerchant(m domain.Merchant) *merchantv1.Merchant {
	return &merchantv1.Merchant{
		Id:                m.ID.String(),
		Slug:              m.Slug,
		Name:              m.Name,
		City:              m.City,
		Address:           m.Address,
		Status:            toProtoStatus(m.Status),
		IsAcceptingOrders: m.IsAcceptingOrders,
		MinOrderMinor:     m.MinOrderMinor,
		DeliveryFeeMinor:  m.DeliveryFeeMinor,
		AvgPrepMinutes:    m.AvgPrepMinutes,
		Version:           m.Version,
	}
}

// toStatusError maps domain errors onto gRPC codes. Anything unmapped becomes
// Internal with a generic message so that storage details never leak.
func toStatusError(err error) error {
	switch {
	case errors.Is(err, domain.ErrMerchantNotFound):
		return status.Error(codes.NotFound, "merchant not found")
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		return status.Error(codes.NotFound, "api key not found")
	case errors.Is(err, domain.ErrVersionConflict):
		return status.Error(codes.Aborted, "merchant version conflict")
	}
	if ve, ok := domain.AsValidationError(err); ok {
		return status.Error(codes.InvalidArgument, ve.Error())
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}
	return status.Error(codes.Internal, "internal error")
}
