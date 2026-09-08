package domain

import "time"

// Mode is how the till answers new orders.
type Mode string

const (
	// ModeAutoAccept confirms every new order. It is the default, because the
	// demonstration has to show the whole life cycle without a human.
	ModeAutoAccept Mode = "AUTO_ACCEPT"
	// ModeAutoReject refuses every new order. It exists to demonstrate the
	// compensating path: the held units come back.
	ModeAutoReject Mode = "AUTO_REJECT"
	// ModeManual leaves new orders alone, so that a person can accept or
	// refuse them through the platform API and watch what happens.
	ModeManual Mode = "MANUAL"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool {
	return m == ModeAutoAccept || m == ModeAutoReject || m == ModeManual
}

// Order statuses of the platform, as the till sees them over HTTP. They are
// plain strings on purpose: partner-pos is an external system and must not
// import the internals of order-service.
const (
	StatusCreated    = "CREATED"
	StatusConfirmed  = "CONFIRMED"
	StatusCooking    = "COOKING"
	StatusDelivering = "DELIVERING"
	StatusDelivered  = "DELIVERED"
	// Терминальные статусы: касса их не выставляет через продвижение, но
	// называет при отказе и видит у отменённых покупателем заказов.
	StatusRejected  = "REJECTED"
	StatusCancelled = "CANCELLED"
)

// WatchedStatuses are the statuses the till polls: the ones it can still act
// on. Terminal orders are of no interest to it.
func WatchedStatuses() []string {
	return []string{StatusCreated, StatusConfirmed, StatusCooking, StatusDelivering}
}

// Bounds of the behaviour settings.
const (
	MaxETAMinutes         = 240
	MaxRejectReasonLength = 200
)

// Behavior is how the till is configured to act right now.
type Behavior struct {
	Mode         Mode
	ETAMinutes   int32
	RejectReason string
	// Kitchen turns the simulation of preparation on: an accepted order is
	// walked through COOKING, DELIVERING and DELIVERED, one step per
	// KitchenStep. With it off the till only accepts or refuses.
	Kitchen bool
}

// DefaultBehavior is what the till starts with.
func DefaultBehavior() Behavior {
	return Behavior{
		Mode:         ModeAutoAccept,
		ETAMinutes:   25,
		RejectReason: "закончились продукты",
		Kitchen:      true,
	}
}

// Validate checks the behaviour settings.
func (b Behavior) Validate() error {
	ve := &ValidationError{}
	if !b.Mode.Valid() {
		ve.Add("mode", "must be AUTO_ACCEPT, AUTO_REJECT or MANUAL")
	}
	if b.ETAMinutes < 0 || b.ETAMinutes > MaxETAMinutes {
		ve.Add("eta_minutes", "must be between 0 and 240")
	}
	if len([]rune(b.RejectReason)) > MaxRejectReasonLength {
		ve.Add("reject_reason", "must not exceed 200 characters")
	}
	if b.Mode == ModeAutoReject && len([]rune(b.RejectReason)) == 0 {
		ve.Add("reject_reason", "is required in AUTO_REJECT mode")
	}
	return ve.OrNil()
}

// Action is what the till does with one order.
type Action string

const (
	// ActionHold means: leave the order as it is for now.
	ActionHold Action = "HOLD"
	// ActionAccept means: confirm the order.
	ActionAccept Action = "ACCEPT"
	// ActionReject means: refuse the order.
	ActionReject Action = "REJECT"
	// ActionAdvance means: move the order one step along preparation.
	ActionAdvance Action = "ADVANCE"
)

// Decision is the outcome for one order.
type Decision struct {
	Action     Action
	ETAMinutes int32
	Reason     string
	// NextStatus is filled for ActionAdvance only.
	NextStatus string
}

// nextStatus is the preparation ladder of the platform.
var nextStatus = map[string]string{
	StatusConfirmed:  StatusCooking,
	StatusCooking:    StatusDelivering,
	StatusDelivering: StatusDelivered,
}

// Decide returns what the till does with an order in the given status.
//
// age is how long the order has been in that status and step is how long one
// preparation stage takes. Driving the ladder from the age of the status makes
// the decision a pure function of what the platform reports: the till keeps no
// state about orders, so restarting it changes nothing and a repeated poll
// cannot move an order twice.
func Decide(b Behavior, status string, age, step time.Duration) Decision {
	switch status {
	case StatusCreated:
		switch b.Mode {
		case ModeAutoAccept:
			return Decision{Action: ActionAccept, ETAMinutes: b.ETAMinutes}
		case ModeAutoReject:
			return Decision{Action: ActionReject, Reason: b.RejectReason}
		default:
			return Decision{Action: ActionHold}
		}
	case StatusConfirmed, StatusCooking, StatusDelivering:
		next, ok := nextStatus[status]
		if !ok || !b.Kitchen || age < step {
			return Decision{Action: ActionHold}
		}
		return Decision{Action: ActionAdvance, NextStatus: next}
	default:
		return Decision{Action: ActionHold}
	}
}
