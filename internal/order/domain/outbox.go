package domain

import (
	"time"

	"github.com/google/uuid"
)

// MessageType is the kind of terminal decision an outbox message carries.
type MessageType string

const (
	// MessageCommitReservation tells inventory-service to consume the held
	// units: the merchant accepted the order.
	MessageCommitReservation MessageType = "RESERVATION_COMMIT"
	// MessageReleaseReservation tells inventory-service to return the held
	// units: the order was refused, cancelled or expired.
	MessageReleaseReservation MessageType = "RESERVATION_RELEASE"
)

// Valid reports whether t is a known message type.
func (t MessageType) Valid() bool {
	return t == MessageCommitReservation || t == MessageReleaseReservation
}

// MessageState is the delivery state of an outbox message.
type MessageState string

const (
	// MessagePending - not delivered yet; the dispatcher will pick it up.
	MessagePending MessageState = "PENDING"
	// MessageSent - the receiver acknowledged the decision.
	MessageSent MessageState = "SENT"
	// MessageFailed - permanently undeliverable; it needs a human.
	MessageFailed MessageState = "FAILED"
)

// ReleaseReason explains why held units return to availability. The values
// match inventory.v1.ReleaseReason.
type ReleaseReason string

const (
	// ReleaseOrderRejected - the merchant refused the order.
	ReleaseOrderRejected ReleaseReason = "ORDER_REJECTED"
	// ReleaseOrderCancelled - the customer cancelled the order.
	ReleaseOrderCancelled ReleaseReason = "ORDER_CANCELLED"
	// ReleaseOrderExpired - the order expired before the merchant answered.
	ReleaseOrderExpired ReleaseReason = "ORDER_EXPIRED"
)

// Valid reports whether r is a reason this service may send.
func (r ReleaseReason) Valid() bool {
	return r == ReleaseOrderRejected || r == ReleaseOrderCancelled || r == ReleaseOrderExpired
}

// Delivery policy of the outbox (TZ v3.0 §8.3).
const (
	// MaxDeliveryAttempts is how many times one message is tried before it is
	// parked as FAILED. The whole retry window stays well inside the 30-minute
	// reservation TTL, so a lost release never holds units forever: the
	// sweeper of inventory-service returns them.
	MaxDeliveryAttempts = 12
	// MaxBackoff caps the exponential delay between attempts.
	MaxBackoff = 300 * time.Second
	// InitialBackoff is the delay before the second attempt.
	InitialBackoff = time.Second
)

// BackoffFor returns the delay before the next attempt after the given number
// of failed attempts: 1s, 2s, 4s, ... capped at MaxBackoff.
func BackoffFor(attempts int32) time.Duration {
	if attempts <= 1 {
		return InitialBackoff
	}
	d := InitialBackoff
	for i := int32(1); i < attempts; i++ {
		d *= 2
		if d >= MaxBackoff {
			return MaxBackoff
		}
	}
	return d
}

// Message is one terminal decision waiting to reach inventory-service.
//
// It is written in the same transaction as the status change it describes,
// which is what makes the pair "decision recorded" and "decision scheduled for
// delivery" atomic. Delivery itself is at-least-once; the receiver is
// idempotent by reservation id and state, so the effect happens exactly once.
// Exactly-once delivery is not claimed.
type Message struct {
	ID            uuid.UUID
	AggregateID   uuid.UUID
	Type          MessageType
	ReservationID uuid.UUID
	Reason        ReleaseReason
	CorrelationID string
	State         MessageState
	Attempts      int32
	AvailableAt   time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewCommitMessage builds the message that consumes the units of an order.
func NewCommitMessage(orderID uuid.UUID, correlationID string) Message {
	return Message{
		ID:            uuid.New(),
		AggregateID:   orderID,
		Type:          MessageCommitReservation,
		ReservationID: orderID,
		CorrelationID: correlationID,
		State:         MessagePending,
	}
}

// NewReleaseMessage builds the message that returns the units of an order.
func NewReleaseMessage(orderID uuid.UUID, reason ReleaseReason, correlationID string) Message {
	return Message{
		ID:            uuid.New(),
		AggregateID:   orderID,
		Type:          MessageReleaseReservation,
		ReservationID: orderID,
		Reason:        reason,
		CorrelationID: correlationID,
		State:         MessagePending,
	}
}

// MessageForTransition returns the outbox message a transition requires, or
// false when the transition changes nothing outside this service. Keeping the
// rule here means the HTTP adapter cannot forget to schedule a release.
func MessageForTransition(orderID uuid.UUID, target Status, correlationID string) (Message, bool) {
	switch target {
	case StatusConfirmed:
		return NewCommitMessage(orderID, correlationID), true
	case StatusRejected:
		return NewReleaseMessage(orderID, ReleaseOrderRejected, correlationID), true
	case StatusCancelled:
		return NewReleaseMessage(orderID, ReleaseOrderCancelled, correlationID), true
	default:
		return Message{}, false
	}
}
