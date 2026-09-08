package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
)

// GetCustomerOrder returns one order of the customer who asks for it. An order
// of another customer is reported as absent rather than as forbidden: the
// platform performs no customer authentication, so confirming that an order
// exists would leak it to anyone who guesses an identifier.
func (s *Service) GetCustomerOrder(ctx context.Context, customerID, orderID uuid.UUID) (domain.Order, error) {
	order, err := s.orders.GetByID(ctx, orderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.CustomerID != customerID {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	return order, nil
}

// ListCustomerOrders returns the order history of one customer.
func (s *Service) ListCustomerOrders(ctx context.Context, customerID uuid.UUID,
	limit, offset int) (OrderPage, error) {
	if customerID == uuid.Nil {
		ve := &domain.ValidationError{}
		ve.Add("X-Customer-Id", "is required")
		return OrderPage{}, ve
	}
	return s.orders.ListByCustomer(ctx, customerID, limit, offset)
}

// GetMerchantOrder returns one order of the merchant that asks for it.
func (s *Service) GetMerchantOrder(ctx context.Context, merchantID, orderID uuid.UUID) (domain.Order, error) {
	order, err := s.orders.GetByID(ctx, orderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.MerchantID != merchantID {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	return order, nil
}

// ListMerchantOrders returns the queue of one merchant, oldest first, so that
// polling it every few seconds shows the orders in the order they arrived.
func (s *Service) ListMerchantOrders(ctx context.Context, merchantID uuid.UUID,
	statuses []domain.Status, limit, offset int) (OrderPage, error) {
	ve := &domain.ValidationError{}
	if merchantID == uuid.Nil {
		ve.Add("merchant_id", "is required")
	}
	for _, st := range statuses {
		if !st.Valid() {
			ve.Add("status", "unknown status "+string(st))
		}
	}
	if err := ve.OrNil(); err != nil {
		return OrderPage{}, err
	}
	return s.orders.ListByMerchant(ctx, merchantID, statuses, limit, offset)
}
