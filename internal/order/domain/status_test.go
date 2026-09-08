package domain_test

import (
	"testing"

	"github.com/avito-kitchen/platform/internal/order/domain"
)

func TestStatusTransitionMatrix(t *testing.T) {
	t.Parallel()
	all := []domain.Status{
		domain.StatusCreated, domain.StatusConfirmed, domain.StatusRejected,
		domain.StatusCancelled, domain.StatusCooking, domain.StatusDelivering,
		domain.StatusDelivered,
	}
	allowed := map[domain.Status]map[domain.Status]bool{
		domain.StatusCreated: {
			domain.StatusConfirmed: true,
			domain.StatusRejected:  true,
			domain.StatusCancelled: true,
		},
		domain.StatusConfirmed:  {domain.StatusCooking: true},
		domain.StatusCooking:    {domain.StatusDelivering: true},
		domain.StatusDelivering: {domain.StatusDelivered: true},
		domain.StatusRejected:   {},
		domain.StatusCancelled:  {},
		domain.StatusDelivered:  {},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			if got := from.CanTransitionTo(to); got != want {
				t.Fatalf("%s -> %s = %v, want %v", from, to, got, want)
			}
		}
	}
}

// A terminal order is final: nothing, not even a repeat of the move that made
// it terminal, may move it further.
func TestTerminalStatusesAcceptNothing(t *testing.T) {
	t.Parallel()
	for _, st := range []domain.Status{
		domain.StatusRejected, domain.StatusCancelled, domain.StatusDelivered,
	} {
		if !st.Terminal() {
			t.Fatalf("%s must be terminal", st)
		}
		if st.CanTransitionTo(domain.StatusCooking) {
			t.Fatalf("%s must not move to COOKING", st)
		}
	}
	if domain.StatusCreated.Terminal() || domain.StatusConfirmed.Terminal() {
		t.Fatal("CREATED and CONFIRMED are not terminal")
	}
}

// Units are held only while the merchant has not decided. Every other status
// has either consumed or returned them, and a second release would be wrong.
func TestOnlyCreatedHoldsUnits(t *testing.T) {
	t.Parallel()
	if !domain.StatusCreated.HoldsUnits() {
		t.Fatal("CREATED must hold units")
	}
	for _, st := range []domain.Status{
		domain.StatusConfirmed, domain.StatusRejected, domain.StatusCancelled,
		domain.StatusCooking, domain.StatusDelivering, domain.StatusDelivered,
	} {
		if st.HoldsUnits() {
			t.Fatalf("%s must not hold units", st)
		}
	}
}

func TestCancellableOnlyBeforeTheDecision(t *testing.T) {
	t.Parallel()
	if !domain.StatusCreated.CancellableByCustomer() {
		t.Fatal("a created order must be cancellable")
	}
	if domain.StatusConfirmed.CancellableByCustomer() {
		t.Fatal("a confirmed order must not be cancellable by the customer")
	}
}

func TestStatusValid(t *testing.T) {
	t.Parallel()
	if !domain.StatusDelivering.Valid() {
		t.Fatal("DELIVERING must be valid")
	}
	if domain.Status("SHIPPED").Valid() {
		t.Fatal("an unknown status must not be valid")
	}
}

// Accepting and refusing carry an estimate and a reason, so they have their own
// endpoints and must not be reachable through the progress one.
func TestMerchantProgressExcludesDecisions(t *testing.T) {
	t.Parallel()
	for _, st := range domain.MerchantProgressStatuses() {
		if !st.IsMerchantProgress() {
			t.Fatalf("%s must be a progress status", st)
		}
	}
	for _, st := range []domain.Status{
		domain.StatusConfirmed, domain.StatusRejected, domain.StatusCancelled,
		domain.StatusCreated,
	} {
		if st.IsMerchantProgress() {
			t.Fatalf("%s must not be reachable through the progress endpoint", st)
		}
	}
}
