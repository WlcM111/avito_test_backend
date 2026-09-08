package domain_test

import (
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/google/uuid"
)

func TestReservationTransitions(t *testing.T) {
	t.Parallel()
	all := []domain.ReservationState{
		domain.ReservationHeld, domain.ReservationCommitted, domain.ReservationReleased,
	}
	allowed := map[domain.ReservationState]map[domain.ReservationState]bool{
		domain.ReservationHeld: {
			domain.ReservationCommitted: true,
			domain.ReservationReleased:  true,
		},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			if got := from.CanTransitionTo(to); got != want {
				t.Fatalf("CanTransitionTo(%s -> %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if domain.ReservationHeld.Terminal() {
		t.Fatal("HELD reported terminal")
	}
	for _, s := range []domain.ReservationState{domain.ReservationCommitted, domain.ReservationReleased} {
		if !s.Terminal() {
			t.Fatalf("%s reported non-terminal", s)
		}
	}
}

func TestReleaseReasonValid(t *testing.T) {
	t.Parallel()
	for _, r := range []domain.ReleaseReason{
		domain.ReleaseOrderRejected, domain.ReleaseOrderCancelled, domain.ReleaseOrderExpired,
	} {
		if !r.Valid() {
			t.Fatalf("%q rejected", r)
		}
	}
	// The sweeper reason is produced internally and must not be accepted from
	// a caller, otherwise a client could disguise a cancellation as an expiry.
	if domain.ReleaseTTLExpired.Valid() {
		t.Fatal("TTL_EXPIRED accepted as a caller-supplied reason")
	}
	if domain.ReleaseReason("WHATEVER").Valid() {
		t.Fatal("unknown reason accepted")
	}
}

func TestNormalizeLinesMergesAndSorts(t *testing.T) {
	t.Parallel()
	high := uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff")
	low := uuid.MustParse("00000001-0000-4000-8000-000000000000")
	mid := uuid.MustParse("88888888-8888-4888-8888-888888888888")

	got := domain.NormalizeLines([]domain.ReserveLine{
		{MenuItemID: high, Qty: 1},
		{MenuItemID: low, Qty: 2},
		{MenuItemID: high, Qty: 3},
		{MenuItemID: mid, Qty: 4},
	})
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3 (duplicates merged)", len(got))
	}
	// The deterministic order is what prevents deadlocks between concurrent
	// multi-item reservations, so it is asserted explicitly.
	if got[0].MenuItemID != low || got[1].MenuItemID != mid || got[2].MenuItemID != high {
		t.Fatalf("lines are not sorted by menu item id: %+v", got)
	}
	if got[2].Qty != 4 {
		t.Fatalf("duplicate quantities were not summed: %d", got[2].Qty)
	}
	if domain.NormalizeLines(nil) != nil {
		t.Fatal("NormalizeLines(nil) must return nil")
	}
}

func TestReserveRequestValidate(t *testing.T) {
	t.Parallel()
	valid := domain.ReserveRequest{
		ReservationID: uuid.New(),
		MerchantID:    uuid.New(),
		Lines:         []domain.ReserveLine{{MenuItemID: uuid.New(), Qty: 2}},
		TTL:           time.Minute,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	cases := map[string]struct {
		mutate func(*domain.ReserveRequest)
		field  string
	}{
		"missing reservation id": {func(r *domain.ReserveRequest) { r.ReservationID = uuid.Nil }, "reservation_id"},
		"missing merchant id":    {func(r *domain.ReserveRequest) { r.MerchantID = uuid.Nil }, "merchant_id"},
		"no lines":               {func(r *domain.ReserveRequest) { r.Lines = nil }, "lines"},
		"zero ttl":               {func(r *domain.ReserveRequest) { r.TTL = 0 }, "ttl_seconds"},
		"zero quantity": {
			func(r *domain.ReserveRequest) { r.Lines[0].Qty = 0 }, "lines.qty",
		},
		"quantity above the line cap": {
			func(r *domain.ReserveRequest) { r.Lines[0].Qty = domain.MaxLineQty + 1 }, "lines.qty",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := domain.ReserveRequest{
				ReservationID: valid.ReservationID,
				MerchantID:    valid.MerchantID,
				Lines:         []domain.ReserveLine{{MenuItemID: uuid.New(), Qty: 2}},
				TTL:           valid.TTL,
			}
			tc.mutate(&req)
			ve, ok := domain.AsValidationError(req.Validate())
			if !ok {
				t.Fatalf("request accepted: %+v", req)
			}
			found := false
			for _, v := range ve.Violations {
				if v.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("violations %+v do not mention %q", ve.Violations, tc.field)
			}
		})
	}
}

func TestReserveRequestTotalUnitsCap(t *testing.T) {
	t.Parallel()
	lines := make([]domain.ReserveLine, 0, 5)
	for range 5 {
		lines = append(lines, domain.ReserveLine{MenuItemID: uuid.New(), Qty: domain.MaxLineQty})
	}
	req := domain.ReserveRequest{
		ReservationID: uuid.New(), MerchantID: uuid.New(),
		Lines: domain.NormalizeLines(lines), TTL: time.Minute,
	}
	if _, ok := domain.AsValidationError(req.Validate()); !ok {
		t.Fatalf("request of %d units was accepted", 5*domain.MaxLineQty)
	}
}

func TestMovementKindValid(t *testing.T) {
	t.Parallel()
	for _, k := range []domain.MovementKind{
		domain.MovementSet, domain.MovementAdjust, domain.MovementReserve,
		domain.MovementRelease, domain.MovementCommit, domain.MovementExpire,
	} {
		if !k.Valid() {
			t.Fatalf("%q reported invalid", k)
		}
	}
	if domain.MovementKind("TRANSFER").Valid() {
		t.Fatal("unknown movement kind reported valid")
	}
}
