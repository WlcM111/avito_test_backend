package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/google/uuid"
)

// CancelOrder withdraws an order the merchant has not decided on yet
// (scenario S-C6).
//
// The status change and the message that returns the held units are written in
// one transaction. The units are not returned here: doing so would mean
// changing state in two systems inside one request, and a failure of the
// second half would leave the order cancelled with its units still held. The
// dispatcher delivers the decision afterwards, retrying until it lands.
func (s *Service) CancelOrder(ctx context.Context, customerID, orderID uuid.UUID) (TransitionResult, error) {
	ve := &domain.ValidationError{}
	if customerID == uuid.Nil {
		ve.Add("X-Customer-Id", "is required")
	}
	if orderID == uuid.Nil {
		ve.Add("order_id", "is required")
	}
	if err := ve.OrNil(); err != nil {
		return TransitionResult{}, err
	}

	message, _ := domain.MessageForTransition(orderID, domain.StatusCancelled, logx.RequestID(ctx))
	return s.orders.Transition(ctx, TransitionCommand{
		OrderID:          orderID,
		ExpectCustomerID: &customerID,
		Target:           domain.StatusCancelled,
		Message:          &message,
	})
}
