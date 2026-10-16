package grpcclient

import (
	"context"
	"time"

	inventoryv1 "github.com/avito-kitchen/platform/gen/grpc/inventory/v1"
	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StockLedger holds, consumes and returns units through inventory-service.
type StockLedger struct {
	client            inventoryv1.InventoryServiceClient
	readTimeout       time.Duration
	reserveTimeout    time.Duration
	transitionTimeout time.Duration
}

// NewStockLedger wires the adapter to an existing client connection. The three
// deadlines follow §7.3 of the specification: a read is cheap, a hold takes a
// row lock, and a terminal decision is delivered by a background dispatcher
// that can afford to wait.
func NewStockLedger(conn grpc.ClientConnInterface,
	readTimeout, reserveTimeout, transitionTimeout time.Duration) *StockLedger {
	if readTimeout <= 0 {
		readTimeout = 500 * time.Millisecond
	}
	if reserveTimeout <= 0 {
		reserveTimeout = 800 * time.Millisecond
	}
	if transitionTimeout <= 0 {
		transitionTimeout = 3 * time.Second
	}
	return &StockLedger{
		client:            inventoryv1.NewInventoryServiceClient(conn),
		readTimeout:       readTimeout,
		reserveTimeout:    reserveTimeout,
		transitionTimeout: transitionTimeout,
	}
}

// GetAvailability reports how many units of each position can still be ordered.
func (l *StockLedger) GetAvailability(ctx context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) ([]app.Availability, error) {
	callCtx, cancel := context.WithTimeout(ctx, l.readTimeout)
	defer cancel()

	ids := make([]string, 0, len(menuItemIDs))
	for _, id := range menuItemIDs {
		ids = append(ids, id.String())
	}
	resp, err := l.client.GetAvailability(callCtx, &inventoryv1.GetAvailabilityRequest{
		MerchantId: merchantID.String(), MenuItemIds: ids,
	})
	if err != nil {
		return nil, dependencyError(ctx, depInventory, err)
	}

	out := make([]app.Availability, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		id, parseErr := uuid.Parse(item.GetMenuItemId())
		if parseErr != nil {
			return nil, app.Unavailable(depInventory, parseErr)
		}
		out = append(out, app.Availability{
			MenuItemID:   id,
			Tracked:      item.GetTracked(),
			SalesStopped: item.GetSalesState() != inventoryv1.SalesState_SALES_STATE_ENABLED,
			AvailableQty: item.GetAvailableQty(),
		})
	}
	return out, nil
}

// Reserve holds the units of every line atomically.
//
// A refusal arrives as FAILED_PRECONDITION with the per-line outcome attached
// to the status details, because gRPC discards the response message of a
// handler that returned an error. The detail is a message of the published
// inventory.v1 contract, so reading it here uses the contract rather than the
// internals of the other service.
func (l *StockLedger) Reserve(ctx context.Context, reservationID, merchantID uuid.UUID,
	lines []domain.Line, ttl time.Duration) (app.ReserveResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, l.reserveTimeout)
	defer cancel()

	protoLines := make([]*inventoryv1.StockLine, 0, len(lines))
	for _, line := range lines {
		protoLines = append(protoLines, &inventoryv1.StockLine{
			MenuItemId: line.MenuItemID.String(), Qty: line.Qty,
		})
	}
	resp, err := l.client.Reserve(callCtx, &inventoryv1.ReserveRequest{
		ReservationId: reservationID.String(),
		MerchantId:    merchantID.String(),
		Lines:         protoLines,
		TtlSeconds:    int32(ttl.Seconds()),
	})
	if err != nil {
		if rejection, ok := reserveRejection(err); ok {
			return app.ReserveResult{}, rejection
		}
		if status.Code(err) == codes.InvalidArgument {
			// Our request is malformed: a defect on our side, not an outage
			// and not a refusal of the basket, so it must not become 503.
			return app.ReserveResult{}, invalidRequest(depInventory, err)
		}
		return app.ReserveResult{}, dependencyError(ctx, depInventory, err)
	}

	var expiresAt time.Time
	if raw := resp.GetExpiresAt(); raw != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
			expiresAt = parsed
		}
	}
	return app.ReserveResult{
		ExpiresAt:      expiresAt,
		AlreadyExisted: resp.GetAlreadyExisted(),
	}, nil
}

// reserveRejection extracts the per-line outcome of a refused hold.
func reserveRejection(err error) (*app.ReserveRejectedError, bool) {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return nil, false
	}
	for _, detail := range st.Details() {
		reserve, isReserve := detail.(*inventoryv1.ReserveResponse)
		if !isReserve {
			continue
		}
		out := &app.ReserveRejectedError{Lines: make([]app.RejectedLine, 0, len(reserve.GetLines()))}
		for _, line := range reserve.GetLines() {
			id, parseErr := uuid.Parse(line.GetMenuItemId())
			if parseErr != nil {
				continue
			}
			out.Lines = append(out.Lines, app.RejectedLine{
				MenuItemID:   id,
				RequestedQty: line.GetRequestedQty(),
				AvailableQty: line.GetAvailableQty(),
				Problem:      fromProtoLineProblem(line.GetProblem()),
			})
		}
		return out, true
	}
	// A refusal without the detail is still a refusal: the customer must not
	// be told the platform is broken when the basket simply cannot be held.
	return &app.ReserveRejectedError{}, true
}

func fromProtoLineProblem(p inventoryv1.LineProblem) domain.ItemProblem {
	switch p {
	case inventoryv1.LineProblem_LINE_PROBLEM_NONE:
		return domain.ItemProblemNone
	case inventoryv1.LineProblem_LINE_PROBLEM_NOT_TRACKED:
		return domain.ItemProblemNotTracked
	case inventoryv1.LineProblem_LINE_PROBLEM_SALES_STOPPED:
		return domain.ItemProblemSalesStopped
	case inventoryv1.LineProblem_LINE_PROBLEM_INSUFFICIENT_UNITS:
		return domain.ItemProblemInsufficientUnits
	default:
		return domain.ItemProblemInsufficientUnits
	}
}

// Commit consumes the units held for an order.
func (l *StockLedger) Commit(ctx context.Context, reservationID uuid.UUID) error {
	callCtx, cancel := context.WithTimeout(ctx, l.transitionTimeout)
	defer cancel()

	_, err := l.client.CommitReservation(callCtx, &inventoryv1.CommitReservationRequest{
		ReservationId: reservationID.String(),
	})
	return l.transitionError(ctx, err)
}

// Release returns the units held for an order to availability.
func (l *StockLedger) Release(ctx context.Context, reservationID uuid.UUID,
	reason domain.ReleaseReason) error {
	callCtx, cancel := context.WithTimeout(ctx, l.transitionTimeout)
	defer cancel()

	protoReason, ok := toProtoReleaseReason(reason)
	if !ok {
		return app.ErrInvalidRequest
	}
	_, err := l.client.ReleaseReservation(callCtx, &inventoryv1.ReleaseReservationRequest{
		ReservationId: reservationID.String(),
		Reason:        protoReason,
	})
	return l.transitionError(ctx, err)
}

// transitionError classifies the answer to a terminal decision. The retry
// policy of the dispatcher depends entirely on this classification: a
// malformed message and an unreachable receiver must not be retried the same
// way.
func (l *StockLedger) transitionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.InvalidArgument:
		return app.ErrInvalidRequest
	case codes.NotFound:
		return app.ErrReservationNotFound
	case codes.FailedPrecondition:
		return app.ErrReservationNotHeld
	default:
		return dependencyError(ctx, depInventory, err)
	}
}

func toProtoReleaseReason(r domain.ReleaseReason) (inventoryv1.ReleaseReason, bool) {
	switch r {
	case domain.ReleaseOrderRejected:
		return inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_REJECTED, true
	case domain.ReleaseOrderCancelled:
		return inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_CANCELLED, true
	case domain.ReleaseOrderExpired:
		return inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_EXPIRED, true
	default:
		return inventoryv1.ReleaseReason_RELEASE_REASON_UNSPECIFIED, false
	}
}

var (
	_ app.MerchantDirectory = (*MerchantDirectory)(nil)
	_ app.MenuCatalogue     = (*MenuCatalogue)(nil)
	_ app.StockLedger       = (*StockLedger)(nil)
)
