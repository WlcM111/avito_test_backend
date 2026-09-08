package app

import (
	"context"
	"strings"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/google/uuid"
)

// AcceptOrder confirms an order and schedules the consumption of the units it
// holds (scenario S-M7). etaMinutes is the merchant's own estimate; zero means
// "use the outlet's average", which the order already carries.
func (s *Service) AcceptOrder(ctx context.Context, merchantID, orderID uuid.UUID,
	etaMinutes int32) (TransitionResult, error) {
	ve := &domain.ValidationError{}
	validateOwners(ve, merchantID, orderID)
	if etaMinutes < 0 || etaMinutes > domain.MaxETAMinutes {
		ve.Add("eta_minutes", "must be between 0 and 240")
	}
	if err := ve.OrNil(); err != nil {
		return TransitionResult{}, err
	}

	message, _ := domain.MessageForTransition(orderID, domain.StatusConfirmed, logx.RequestID(ctx))
	return s.orders.Transition(ctx, TransitionCommand{
		OrderID:          orderID,
		ExpectMerchantID: &merchantID,
		Target:           domain.StatusConfirmed,
		ETAMinutes:       etaMinutes,
		Message:          &message,
	})
}

// RejectOrder refuses an order and schedules the return of the units it holds.
func (s *Service) RejectOrder(ctx context.Context, merchantID, orderID uuid.UUID,
	reason string) (TransitionResult, error) {
	ve := &domain.ValidationError{}
	validateOwners(ve, merchantID, orderID)
	reason = strings.TrimSpace(reason)
	switch n := len([]rune(reason)); {
	case n == 0:
		ve.Add("reason", "is required")
	case n > domain.MaxRejectReasonLength:
		ve.Add("reason", "must not exceed 200 characters")
	}
	if err := ve.OrNil(); err != nil {
		return TransitionResult{}, err
	}

	message, _ := domain.MessageForTransition(orderID, domain.StatusRejected, logx.RequestID(ctx))
	return s.orders.Transition(ctx, TransitionCommand{
		OrderID:          orderID,
		ExpectMerchantID: &merchantID,
		Target:           domain.StatusRejected,
		RejectReason:     reason,
		Message:          &message,
	})
}

// AdvanceOrder moves a confirmed order through preparation and delivery
// (scenario S-M8). These transitions change nothing outside this service, so
// they schedule no message.
func (s *Service) AdvanceOrder(ctx context.Context, merchantID, orderID uuid.UUID,
	target domain.Status) (TransitionResult, error) {
	ve := &domain.ValidationError{}
	validateOwners(ve, merchantID, orderID)
	if !target.IsMerchantProgress() {
		ve.Add("status", "must be COOKING, DELIVERING or DELIVERED")
	}
	if err := ve.OrNil(); err != nil {
		return TransitionResult{}, err
	}
	return s.orders.Transition(ctx, TransitionCommand{
		OrderID:          orderID,
		ExpectMerchantID: &merchantID,
		Target:           target,
	})
}

func validateOwners(ve *domain.ValidationError, merchantID, orderID uuid.UUID) {
	if merchantID == uuid.Nil {
		ve.Add("merchant_id", "is required")
	}
	if orderID == uuid.Nil {
		ve.Add("order_id", "is required")
	}
}
