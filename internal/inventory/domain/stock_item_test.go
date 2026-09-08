package domain_test

import (
	"errors"
	"testing"

	"github.com/avito-kitchen/platform/internal/inventory/domain"
)

func TestAvailableQty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		onHand        int32
		reserved      int32
		wantAvailable int32
	}{
		{"nothing held", 10, 0, 10},
		{"partly held", 10, 4, 6},
		{"fully held", 3, 3, 0},
		{"empty card", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := domain.StockItem{OnHandQty: tc.onHand, ReservedQty: tc.reserved}
			if got := item.AvailableQty(); got != tc.wantAvailable {
				t.Fatalf("AvailableQty() = %d, want %d", got, tc.wantAvailable)
			}
		})
	}
}

// Enabling sales must never confirm units: an ENABLED card with zero units is
// not sellable. This is the central rule of the per-unit accounting model.
func TestSellableRequiresBothDimensions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		state    domain.SalesState
		onHand   int32
		reserved int32
		want     bool
	}{
		{"enabled with units", domain.SalesEnabled, 5, 0, true},
		{"enabled with zero units", domain.SalesEnabled, 0, 0, false},
		{"enabled but everything held", domain.SalesEnabled, 3, 3, false},
		{"stopped with units", domain.SalesStopped, 5, 0, false},
		{"stopped with zero units", domain.SalesStopped, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := domain.StockItem{
				SalesState: tc.state, OnHandQty: tc.onHand, ReservedQty: tc.reserved,
			}
			if got := item.Sellable(); got != tc.want {
				t.Fatalf("Sellable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanReserve(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		state    domain.SalesState
		onHand   int32
		reserved int32
		qty      int32
		want     domain.LineProblem
	}{
		{"enough units", domain.SalesEnabled, 10, 2, 5, domain.LineProblemNone},
		{"exactly enough", domain.SalesEnabled, 10, 5, 5, domain.LineProblemNone},
		{"one unit short", domain.SalesEnabled, 10, 6, 5, domain.LineProblemInsufficientUnits},
		{"stop list wins over units", domain.SalesStopped, 100, 0, 1, domain.LineProblemSalesStopped},
		{"empty enabled card", domain.SalesEnabled, 0, 0, 1, domain.LineProblemInsufficientUnits},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := domain.StockItem{
				SalesState: tc.state, OnHandQty: tc.onHand, ReservedQty: tc.reserved,
			}
			if got := item.CanReserve(tc.qty); got != tc.want {
				t.Fatalf("CanReserve(%d) = %s, want %s", tc.qty, got, tc.want)
			}
		})
	}
}

func TestValidateOnHand(t *testing.T) {
	t.Parallel()
	if err := domain.ValidateOnHand(0); err != nil {
		t.Fatalf("zero rejected: %v", err)
	}
	if err := domain.ValidateOnHand(domain.MaxOnHandQty); err != nil {
		t.Fatalf("upper bound rejected: %v", err)
	}
	for _, qty := range []int32{-1, domain.MaxOnHandQty + 1} {
		ve, ok := domain.AsValidationError(domain.ValidateOnHand(qty))
		if !ok {
			t.Fatalf("ValidateOnHand(%d) accepted", qty)
		}
		if ve.Violations[0].Field != "on_hand_qty" {
			t.Fatalf("violation field = %q", ve.Violations[0].Field)
		}
	}
}

func TestValidateAdjustment(t *testing.T) {
	t.Parallel()
	if err := domain.ValidateAdjustment(5, "op-1", "delivery"); err != nil {
		t.Fatalf("valid adjustment rejected: %v", err)
	}
	cases := map[string]struct {
		delta int32
		key   string
		field string
	}{
		"zero delta":          {0, "op-1", "delta"},
		"missing key":         {5, "", "operation_key"},
		"delta above the cap": {domain.MaxOnHandQty + 1, "op-1", "delta"},
		"delta below the cap": {-domain.MaxOnHandQty - 1, "op-1", "delta"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ve, ok := domain.AsValidationError(domain.ValidateAdjustment(tc.delta, tc.key, ""))
			if !ok {
				t.Fatalf("adjustment accepted: %+v", tc)
			}
			if ve.Violations[0].Field != tc.field {
				t.Fatalf("violation field = %q, want %q", ve.Violations[0].Field, tc.field)
			}
		})
	}
}

func TestApplyAdjustment(t *testing.T) {
	t.Parallel()
	item := domain.StockItem{OnHandQty: 10, ReservedQty: 4}

	next, err := item.ApplyAdjustment(5)
	if err != nil || next != 15 {
		t.Fatalf("ApplyAdjustment(5) = %d, %v", next, err)
	}
	next, err = item.ApplyAdjustment(-6)
	if err != nil || next != 4 {
		t.Fatalf("ApplyAdjustment(-6) = %d, %v", next, err)
	}

	// Dropping below the held units must be refused, never clamped: silently
	// losing reservations would break the accounting.
	if _, err := item.ApplyAdjustment(-7); !errors.Is(err, domain.ErrBelowReserved) {
		t.Fatalf("ApplyAdjustment(-7) = %v, want ErrBelowReserved", err)
	}
	if _, err := item.ApplyAdjustment(-11); !errors.Is(err, domain.ErrNegativeResult) {
		t.Fatalf("ApplyAdjustment(-11) = %v, want ErrNegativeResult", err)
	}
	if _, err := item.ApplyAdjustment(domain.MaxOnHandQty); err == nil {
		t.Fatal("adjustment above the cap was accepted")
	}
}

func TestSalesStateValid(t *testing.T) {
	t.Parallel()
	for _, s := range []domain.SalesState{domain.SalesEnabled, domain.SalesStopped} {
		if !s.Valid() {
			t.Fatalf("%q reported invalid", s)
		}
	}
	if domain.SalesState("PAUSED").Valid() {
		t.Fatal("unknown sales state reported valid")
	}
}
