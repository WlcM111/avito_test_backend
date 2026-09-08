package domain

// Status is the lifecycle position of an order (TZ v3.0 §6.5).
type Status string

const (
	// StatusCreated - units are held, the merchant has not decided yet.
	StatusCreated Status = "CREATED"
	// StatusConfirmed - the merchant accepted; the held units are consumed.
	StatusConfirmed Status = "CONFIRMED"
	// StatusRejected - the merchant refused; the held units are returned.
	StatusRejected Status = "REJECTED"
	// StatusCancelled - the customer cancelled before the decision; the held
	// units are returned.
	StatusCancelled Status = "CANCELLED"
	// StatusCooking - the merchant started preparing the order.
	StatusCooking Status = "COOKING"
	// StatusDelivering - the order left the outlet.
	StatusDelivering Status = "DELIVERING"
	// StatusDelivered - the order reached the customer.
	StatusDelivered Status = "DELIVERED"
)

// transitions is the complete matrix of allowed moves. Anything not listed
// here is refused with a conflict rather than silently ignored.
var transitions = map[Status][]Status{
	StatusCreated:    {StatusConfirmed, StatusRejected, StatusCancelled},
	StatusConfirmed:  {StatusCooking},
	StatusCooking:    {StatusDelivering},
	StatusDelivering: {StatusDelivered},
	StatusRejected:   nil,
	StatusCancelled:  nil,
	StatusDelivered:  nil,
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Terminal reports whether no further transition is possible.
func (s Status) Terminal() bool {
	return s == StatusRejected || s == StatusCancelled || s == StatusDelivered
}

// CanTransitionTo reports whether the move is allowed.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// HoldsUnits reports whether units of this order are still held by
// inventory-service. Only an order waiting for the merchant's decision does.
func (s Status) HoldsUnits() bool { return s == StatusCreated }

// CancellableByCustomer reports whether the customer may still cancel. After
// the merchant has decided, cancellation is no longer the customer's call.
func (s Status) CancellableByCustomer() bool { return s == StatusCreated }

// MerchantProgressStatuses lists the statuses a merchant may set through the
// progress endpoint. Accepting and refusing an order have their own endpoints
// because they carry an estimate and a reason.
func MerchantProgressStatuses() []Status {
	return []Status{StatusCooking, StatusDelivering, StatusDelivered}
}

// IsMerchantProgress reports whether s may be set through the progress
// endpoint.
func (s Status) IsMerchantProgress() bool {
	for _, allowed := range MerchantProgressStatuses() {
		if allowed == s {
			return true
		}
	}
	return false
}
