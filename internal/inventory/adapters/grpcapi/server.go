// Package grpcapi is the inbound gRPC adapter of inventory-service. It serves
// inventory.v1.InventoryService for order-service and translates domain errors
// into gRPC status codes. It contains no business rules.
package grpcapi

import (
	"context"
	"errors"
	"time"

	inventoryv1 "github.com/avito-kitchen/platform/gen/grpc/inventory/v1"
	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServiceName is the fully qualified gRPC service name, used for health
// reporting and for client configuration in order-service.
const ServiceName = "inventory.v1.InventoryService"

// Server implements inventory.v1.InventoryService.
type Server struct {
	inventoryv1.UnimplementedInventoryServiceServer
	svc *app.Service
}

// NewServer wires the gRPC adapter to the use cases.
func NewServer(svc *app.Service) *Server { return &Server{svc: svc} }

// Register adds the service to a gRPC registrar.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	inventoryv1.RegisterInventoryServiceServer(reg, s)
}

// GetAvailability reports the current per-unit availability. Read-only.
func (s *Server) GetAvailability(ctx context.Context,
	req *inventoryv1.GetAvailabilityRequest) (*inventoryv1.GetAvailabilityResponse, error) {
	merchantID, err := uuid.Parse(req.GetMerchantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "merchant_id must be a UUID")
	}
	ids, err := parseUUIDs(req.GetMenuItemIds(), "menu_item_ids")
	if err != nil {
		return nil, err
	}
	items, err := s.svc.GetAvailability(ctx, merchantID, ids)
	if err != nil {
		return nil, toStatusError(err)
	}
	out := make([]*inventoryv1.Availability, 0, len(items))
	for _, item := range items {
		out = append(out, &inventoryv1.Availability{
			MenuItemId:   item.MenuItemID.String(),
			SalesState:   toProtoSalesState(item.SalesState),
			OnHandQty:    item.OnHandQty,
			ReservedQty:  item.ReservedQty,
			AvailableQty: item.Available,
			Tracked:      item.Tracked,
			Version:      item.Version,
		})
	}
	return &inventoryv1.GetAvailabilityResponse{Items: out}, nil
}

// Reserve holds the requested units for all lines atomically.
func (s *Server) Reserve(ctx context.Context,
	req *inventoryv1.ReserveRequest) (*inventoryv1.ReserveResponse, error) {
	reservationID, err := uuid.Parse(req.GetReservationId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "reservation_id must be a UUID")
	}
	merchantID, err := uuid.Parse(req.GetMerchantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "merchant_id must be a UUID")
	}
	lines := make([]domain.ReserveLine, 0, len(req.GetLines()))
	for _, l := range req.GetLines() {
		menuItemID, parseErr := uuid.Parse(l.GetMenuItemId())
		if parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "lines.menu_item_id must be a UUID")
		}
		lines = append(lines, domain.ReserveLine{MenuItemID: menuItemID, Qty: l.GetQty()})
	}

	outcome, err := s.svc.Reserve(ctx, reservationID, merchantID, lines,
		time.Duration(req.GetTtlSeconds())*time.Second)
	if err != nil {
		// A rejected hold is reported as FAILED_PRECONDITION with the per-line
		// detail attached to the status. gRPC discards the response message
		// when a handler returns an error, so the detail has to travel in the
		// status itself for the caller to see which lines failed and why.
		if rejected, ok := domain.AsReservationRejected(err); ok {
			return nil, rejectionStatus(rejected)
		}
		return nil, toStatusError(err)
	}

	res := outcome.Reservation
	lineResults := make([]*inventoryv1.ReservationLineResult, 0, len(res.Lines))
	for _, l := range res.Lines {
		lineResults = append(lineResults, &inventoryv1.ReservationLineResult{
			MenuItemId:   l.MenuItemID.String(),
			RequestedQty: l.Qty,
			Problem:      inventoryv1.LineProblem_LINE_PROBLEM_NONE,
		})
	}
	return &inventoryv1.ReserveResponse{
		Reserved:       res.State == domain.ReservationHeld,
		AlreadyExisted: outcome.AlreadyExisted,
		State:          toProtoReservationState(res.State),
		ExpiresAt:      res.ExpiresAt.UTC().Format(time.RFC3339),
		Lines:          lineResults,
	}, nil
}

// CommitReservation consumes the held units.
func (s *Server) CommitReservation(ctx context.Context,
	req *inventoryv1.CommitReservationRequest) (*inventoryv1.CommitReservationResponse, error) {
	reservationID, err := uuid.Parse(req.GetReservationId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "reservation_id must be a UUID")
	}
	outcome, err := s.svc.Commit(ctx, reservationID)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &inventoryv1.CommitReservationResponse{
		State:            toProtoReservationState(outcome.State),
		AlreadyCommitted: outcome.AlreadyApplied,
	}, nil
}

// ReleaseReservation returns the held units to availability.
func (s *Server) ReleaseReservation(ctx context.Context,
	req *inventoryv1.ReleaseReservationRequest) (*inventoryv1.ReleaseReservationResponse, error) {
	reservationID, err := uuid.Parse(req.GetReservationId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "reservation_id must be a UUID")
	}
	reason, err := fromProtoReleaseReason(req.GetReason())
	if err != nil {
		return nil, err
	}
	outcome, err := s.svc.Release(ctx, reservationID, reason)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &inventoryv1.ReleaseReservationResponse{
		State:           toProtoReservationState(outcome.State),
		AlreadyReleased: outcome.AlreadyApplied,
	}, nil
}

// rejectionStatus builds the FAILED_PRECONDITION status of a rejected hold and
// attaches the per-line outcome as a status detail. If attaching fails the
// status is still returned, only without the detail: losing the explanation
// must not turn a well-defined rejection into an internal error.
func rejectionStatus(rejected *domain.ReservationRejectedError) error {
	st := status.New(codes.FailedPrecondition, "reservation rejected")
	detail := &inventoryv1.ReserveResponse{
		Reserved: false,
		State:    inventoryv1.ReservationState_RESERVATION_STATE_UNSPECIFIED,
		Lines:    toProtoLineResults(rejected.Lines),
	}
	withDetails, err := st.WithDetails(detail)
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// RejectionDetail extracts the per-line outcome from a Reserve error. It is
// exported so that order-service reads the detail through the same code that
// produced it instead of re-implementing the extraction.
func RejectionDetail(err error) (*inventoryv1.ReserveResponse, bool) {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return nil, false
	}
	for _, d := range st.Details() {
		if detail, isReserve := d.(*inventoryv1.ReserveResponse); isReserve {
			return detail, true
		}
	}
	return nil, false
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

func toProtoSalesState(s domain.SalesState) inventoryv1.SalesState {
	switch s {
	case domain.SalesEnabled:
		return inventoryv1.SalesState_SALES_STATE_ENABLED
	case domain.SalesStopped:
		return inventoryv1.SalesState_SALES_STATE_STOPPED
	default:
		return inventoryv1.SalesState_SALES_STATE_UNSPECIFIED
	}
}

func toProtoReservationState(s domain.ReservationState) inventoryv1.ReservationState {
	switch s {
	case domain.ReservationHeld:
		return inventoryv1.ReservationState_RESERVATION_STATE_HELD
	case domain.ReservationCommitted:
		return inventoryv1.ReservationState_RESERVATION_STATE_COMMITTED
	case domain.ReservationReleased:
		return inventoryv1.ReservationState_RESERVATION_STATE_RELEASED
	default:
		return inventoryv1.ReservationState_RESERVATION_STATE_UNSPECIFIED
	}
}

func toProtoLineProblem(p domain.LineProblem) inventoryv1.LineProblem {
	switch p {
	case domain.LineProblemNone:
		return inventoryv1.LineProblem_LINE_PROBLEM_NONE
	case domain.LineProblemNotTracked:
		return inventoryv1.LineProblem_LINE_PROBLEM_NOT_TRACKED
	case domain.LineProblemSalesStopped:
		return inventoryv1.LineProblem_LINE_PROBLEM_SALES_STOPPED
	case domain.LineProblemInsufficientUnits:
		return inventoryv1.LineProblem_LINE_PROBLEM_INSUFFICIENT_UNITS
	default:
		return inventoryv1.LineProblem_LINE_PROBLEM_UNSPECIFIED
	}
}

func toProtoLineResults(lines []domain.LineResult) []*inventoryv1.ReservationLineResult {
	out := make([]*inventoryv1.ReservationLineResult, 0, len(lines))
	for _, l := range lines {
		out = append(out, &inventoryv1.ReservationLineResult{
			MenuItemId:   l.MenuItemID.String(),
			RequestedQty: l.RequestedQty,
			AvailableQty: l.AvailableQty,
			Problem:      toProtoLineProblem(l.Problem),
		})
	}
	return out
}

func fromProtoReleaseReason(r inventoryv1.ReleaseReason) (domain.ReleaseReason, error) {
	switch r {
	case inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_REJECTED:
		return domain.ReleaseOrderRejected, nil
	case inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_CANCELLED:
		return domain.ReleaseOrderCancelled, nil
	case inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_EXPIRED:
		return domain.ReleaseOrderExpired, nil
	default:
		return "", status.Error(codes.InvalidArgument,
			"reason must be ORDER_REJECTED, ORDER_CANCELLED or ORDER_EXPIRED")
	}
}

// toStatusError maps domain and application errors onto gRPC codes. Anything
// unmapped becomes Internal with a generic message so storage details never leak.
func toStatusError(err error) error {
	switch {
	case errors.Is(err, domain.ErrReservationNotFound):
		return status.Error(codes.NotFound, "reservation not found")
	case errors.Is(err, domain.ErrStockItemNotFound):
		return status.Error(codes.NotFound, "stock item not found")
	case errors.Is(err, domain.ErrReservationNotHeld):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, domain.ErrBelowReserved):
		return status.Error(codes.FailedPrecondition, "on hand quantity is below the reserved quantity")
	case errors.Is(err, domain.ErrVersionConflict):
		return status.Error(codes.Aborted, "stock item version conflict")
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
