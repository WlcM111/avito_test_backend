package domain

import (
	"time"

	"github.com/google/uuid"
)

// MovementKind classifies one recorded change of quantities.
type MovementKind string

const (
	// MovementSet - the merchant set an absolute on-hand value.
	MovementSet MovementKind = "SET"
	// MovementAdjust - the merchant applied a relative change.
	MovementAdjust MovementKind = "ADJUST"
	// MovementReserve - the platform held units for an order.
	MovementReserve MovementKind = "RESERVE"
	// MovementRelease - held units returned to availability.
	MovementRelease MovementKind = "RELEASE"
	// MovementCommit - held units were consumed by a confirmed order.
	MovementCommit MovementKind = "COMMIT"
	// MovementExpire - the sweeper released a hold that outlived its TTL.
	MovementExpire MovementKind = "EXPIRE"
)

// Valid reports whether k is a known movement kind.
func (k MovementKind) Valid() bool {
	switch k {
	case MovementSet, MovementAdjust, MovementReserve,
		MovementRelease, MovementCommit, MovementExpire:
		return true
	default:
		return false
	}
}

// Movement is one append-only journal entry. Together the entries reconstruct
// the history of a unit count without identifying individual units.
type Movement struct {
	ID            int64
	StockItemID   uuid.UUID
	MenuItemID    uuid.UUID
	Kind          MovementKind
	DeltaOnHand   int32
	DeltaReserved int32
	OnHandAfter   int32
	ReservedAfter int32
	ReservationID *uuid.UUID
	OperationKey  string
	Reason        string
	CreatedAt     time.Time
}
