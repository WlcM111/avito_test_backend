package domain

import (
	"time"

	"github.com/google/uuid"
)

// Status is the platform-level state of a merchant. Only an operator changes
// it; it expresses membership of the closed list of connected merchants.
type Status string

const (
	// StatusActive means the merchant is connected and may trade.
	StatusActive Status = "ACTIVE"
	// StatusSuspended means the operator disconnected the merchant.
	StatusSuspended Status = "SUSPENDED"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return s == StatusActive || s == StatusSuspended }

// Bounds of the commercial profile fields. They are business limits, not
// storage limits, and are enforced in the domain as well as by CHECK
// constraints in the schema.
const (
	MinOrderMinorMax    int64 = 10_000_000 // 100 000.00 RUB
	DeliveryFeeMinorMax int64 = 1_000_000  // 10 000.00 RUB
	AvgPrepMinutesMin   int32 = 1
	AvgPrepMinutesMax   int32 = 240
	MaxCityLength             = 100
	MaxQueryLength            = 100
)

// Merchant is a food outlet connected to the platform. merchant-service is the
// only owner of this entity; other services refer to it by ID.
type Merchant struct {
	ID          uuid.UUID
	Slug        string
	Name        string
	Description string
	Cuisine     string
	City        string
	Address     string
	Phone       string

	Status            Status
	IsAcceptingOrders bool

	MinOrderMinor    int64
	DeliveryFeeMinor int64
	AvgPrepMinutes   int32

	// Version increases on every profile change and drives optimistic
	// concurrency control for PATCH requests.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CanAcceptOrders reports whether an order may be placed with this merchant
// right now. Both the platform status and the merchant's own switch must allow
// it; the two are independent on purpose.
func (m Merchant) CanAcceptOrders() bool {
	return m.Status == StatusActive && m.IsAcceptingOrders
}

// ProfilePatch is a partial update of the commercial profile. A nil field
// means "leave unchanged", which is what distinguishes PATCH from PUT.
type ProfilePatch struct {
	IsAcceptingOrders *bool
	MinOrderMinor     *int64
	DeliveryFeeMinor  *int64
	AvgPrepMinutes    *int32
}

// IsEmpty reports whether the patch carries no fields at all.
func (p ProfilePatch) IsEmpty() bool {
	return p.IsAcceptingOrders == nil && p.MinOrderMinor == nil &&
		p.DeliveryFeeMinor == nil && p.AvgPrepMinutes == nil
}

// Validate checks every present field against the business bounds and reports
// all violations at once.
func (p ProfilePatch) Validate() error {
	if p.IsEmpty() {
		return ErrNoChanges
	}
	ve := &ValidationError{}
	if p.MinOrderMinor != nil {
		if *p.MinOrderMinor < 0 {
			ve.Add("min_order_minor", "must not be negative")
		} else if *p.MinOrderMinor > MinOrderMinorMax {
			ve.Add("min_order_minor", "must not exceed 10000000 minor units")
		}
	}
	if p.DeliveryFeeMinor != nil {
		if *p.DeliveryFeeMinor < 0 {
			ve.Add("delivery_fee_minor", "must not be negative")
		} else if *p.DeliveryFeeMinor > DeliveryFeeMinorMax {
			ve.Add("delivery_fee_minor", "must not exceed 1000000 minor units")
		}
	}
	if p.AvgPrepMinutes != nil {
		if *p.AvgPrepMinutes < AvgPrepMinutesMin || *p.AvgPrepMinutes > AvgPrepMinutesMax {
			ve.Add("avg_prep_minutes", "must be between 1 and 240")
		}
	}
	return ve.OrNil()
}

// Apply writes the present patch fields onto the merchant and reports whether
// anything actually changed. It does not touch Version: the storage layer
// increments it together with the write.
func (m *Merchant) Apply(p ProfilePatch) bool {
	changed := false
	if p.IsAcceptingOrders != nil && *p.IsAcceptingOrders != m.IsAcceptingOrders {
		m.IsAcceptingOrders = *p.IsAcceptingOrders
		changed = true
	}
	if p.MinOrderMinor != nil && *p.MinOrderMinor != m.MinOrderMinor {
		m.MinOrderMinor = *p.MinOrderMinor
		changed = true
	}
	if p.DeliveryFeeMinor != nil && *p.DeliveryFeeMinor != m.DeliveryFeeMinor {
		m.DeliveryFeeMinor = *p.DeliveryFeeMinor
		changed = true
	}
	if p.AvgPrepMinutes != nil && *p.AvgPrepMinutes != m.AvgPrepMinutes {
		m.AvgPrepMinutes = *p.AvgPrepMinutes
		changed = true
	}
	return changed
}

// ListFilter selects and paginates the public merchant list.
type ListFilter struct {
	City          string
	Query         string
	OnlyAccepting bool
	Limit         int
	Offset        int
}

// Validate checks the filter bounds.
func (f ListFilter) Validate() error {
	ve := &ValidationError{}
	if len([]rune(f.City)) > MaxCityLength {
		ve.Add("city", "must not exceed 100 characters")
	}
	if len([]rune(f.Query)) > MaxQueryLength {
		ve.Add("q", "must not exceed 100 characters")
	}
	if f.Limit <= 0 {
		ve.Add("limit", "must be greater than zero")
	}
	if f.Offset < 0 {
		ve.Add("offset", "must not be negative")
	}
	return ve.OrNil()
}
