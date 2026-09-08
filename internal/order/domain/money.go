package domain

// Currency is the only currency of the platform. Amounts are whole minor units
// (kopecks) of it: floating point money would make the CHECK constraint
// total = items + delivery unreliable.
const Currency = "RUB"

// Bounds of the order. They are business limits and are enforced in the domain
// as well as by CHECK constraints in the schema.
const (
	// MinLineQty and MaxLineQty bound the quantity of one position.
	MinLineQty = 1
	MaxLineQty = 99
	// MaxOrderLines bounds the number of distinct positions in one order. It
	// matches the reservation limit of inventory-service, so an order that
	// passes validation can always be reserved.
	MaxOrderLines = 50
	// MaxOrderUnits bounds the total number of units in one order, again to
	// match what inventory-service accepts in one reservation.
	MaxOrderUnits = 200
	// MaxUnitPriceMinor bounds one unit price at 1 000 000 RUB in kopecks.
	MaxUnitPriceMinor = 100_000_000
	// MaxOrderTotalMinor bounds the total of one order.
	MaxOrderTotalMinor = 1_000_000_000
	// MaxETAMinutes bounds the preparation estimate a merchant may promise.
	MaxETAMinutes = 240
	// MaxIdempotencyKeyLength bounds the Idempotency-Key header.
	MaxIdempotencyKeyLength = 128
	// MaxRejectReasonLength bounds the free-text reason of a refusal.
	MaxRejectReasonLength = 200
	// MaxCommentLength bounds the free-text comment of a customer.
	MaxCommentLength = 500
)

// LineTotalMinor is the amount of one position. It is computed in exactly one
// place because the schema checks the same equality.
func LineTotalMinor(unitPriceMinor int64, qty int32) int64 {
	return unitPriceMinor * int64(qty)
}

// TotalMinor is the amount the customer pays: positions plus delivery.
func TotalMinor(itemsTotalMinor, deliveryFeeMinor int64) int64 {
	return itemsTotalMinor + deliveryFeeMinor
}
