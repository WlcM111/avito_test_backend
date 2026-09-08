package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
)

func ptr[T any](v T) *T { return &v }

func TestCanAcceptOrders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		status    domain.Status
		accepting bool
		want      bool
	}{
		{"active and accepting", domain.StatusActive, true, true},
		{"active but switched off", domain.StatusActive, false, false},
		{"suspended but switched on", domain.StatusSuspended, true, false},
		{"suspended and switched off", domain.StatusSuspended, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := domain.Merchant{Status: tc.status, IsAcceptingOrders: tc.accepting}
			if got := m.CanAcceptOrders(); got != tc.want {
				t.Fatalf("CanAcceptOrders() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProfilePatchValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		patch      domain.ProfilePatch
		wantErrIs  error
		wantFields []string
	}{
		{
			name:      "empty patch is rejected",
			patch:     domain.ProfilePatch{},
			wantErrIs: domain.ErrNoChanges,
		},
		{
			name:  "valid patch passes",
			patch: domain.ProfilePatch{MinOrderMinor: ptr(int64(50000)), AvgPrepMinutes: ptr(int32(30))},
		},
		{
			name:       "negative minimum order",
			patch:      domain.ProfilePatch{MinOrderMinor: ptr(int64(-1))},
			wantFields: []string{"min_order_minor"},
		},
		{
			name:       "minimum order above bound",
			patch:      domain.ProfilePatch{MinOrderMinor: ptr(domain.MinOrderMinorMax + 1)},
			wantFields: []string{"min_order_minor"},
		},
		{
			name:       "negative delivery fee",
			patch:      domain.ProfilePatch{DeliveryFeeMinor: ptr(int64(-100))},
			wantFields: []string{"delivery_fee_minor"},
		},
		{
			name:       "preparation time below bound",
			patch:      domain.ProfilePatch{AvgPrepMinutes: ptr(int32(0))},
			wantFields: []string{"avg_prep_minutes"},
		},
		{
			name:       "preparation time above bound",
			patch:      domain.ProfilePatch{AvgPrepMinutes: ptr(domain.AvgPrepMinutesMax + 1)},
			wantFields: []string{"avg_prep_minutes"},
		},
		{
			name: "all violations are reported at once",
			patch: domain.ProfilePatch{
				MinOrderMinor:    ptr(int64(-1)),
				DeliveryFeeMinor: ptr(int64(-1)),
				AvgPrepMinutes:   ptr(int32(999)),
			},
			wantFields: []string{"min_order_minor", "delivery_fee_minor", "avg_prep_minutes"},
		},
		{
			name:  "boundary values are accepted",
			patch: domain.ProfilePatch{MinOrderMinor: ptr(int64(0)), DeliveryFeeMinor: ptr(domain.DeliveryFeeMinorMax), AvgPrepMinutes: ptr(domain.AvgPrepMinutesMin)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.patch.Validate()
			switch {
			case tc.wantErrIs != nil:
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("Validate() = %v, want %v", err, tc.wantErrIs)
				}
			case len(tc.wantFields) > 0:
				ve, ok := domain.AsValidationError(err)
				if !ok {
					t.Fatalf("Validate() = %v, want *ValidationError", err)
				}
				if len(ve.Violations) != len(tc.wantFields) {
					t.Fatalf("got %d violations, want %d: %v", len(ve.Violations), len(tc.wantFields), ve)
				}
				for i, field := range tc.wantFields {
					if ve.Violations[i].Field != field {
						t.Fatalf("violation %d field = %q, want %q", i, ve.Violations[i].Field, field)
					}
				}
			default:
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
			}
		})
	}
}

func TestProfilePatchApply(t *testing.T) {
	t.Parallel()
	base := domain.Merchant{
		IsAcceptingOrders: false,
		MinOrderMinor:     50000,
		DeliveryFeeMinor:  19900,
		AvgPrepMinutes:    35,
	}

	t.Run("absent fields stay untouched", func(t *testing.T) {
		t.Parallel()
		m := base
		changed := m.Apply(domain.ProfilePatch{IsAcceptingOrders: ptr(true)})
		if !changed {
			t.Fatal("Apply() reported no change")
		}
		if !m.IsAcceptingOrders {
			t.Fatal("is_accepting_orders was not applied")
		}
		if m.MinOrderMinor != base.MinOrderMinor || m.DeliveryFeeMinor != base.DeliveryFeeMinor ||
			m.AvgPrepMinutes != base.AvgPrepMinutes {
			t.Fatalf("absent fields were modified: %+v", m)
		}
	})

	t.Run("identical values report no change", func(t *testing.T) {
		t.Parallel()
		m := base
		if m.Apply(domain.ProfilePatch{MinOrderMinor: ptr(base.MinOrderMinor)}) {
			t.Fatal("Apply() reported a change for an identical value")
		}
	})

	t.Run("apply never touches version", func(t *testing.T) {
		t.Parallel()
		m := base
		m.Version = 7
		m.Apply(domain.ProfilePatch{AvgPrepMinutes: ptr(int32(60))})
		if m.Version != 7 {
			t.Fatalf("Version = %d, want 7 (storage owns the increment)", m.Version)
		}
	})
}

func TestListFilterValidate(t *testing.T) {
	t.Parallel()
	valid := domain.ListFilter{Limit: 20, Offset: 0}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}
	cases := map[string]domain.ListFilter{
		"limit":  {Limit: 0},
		"offset": {Limit: 10, Offset: -1},
		"city":   {Limit: 10, City: strings.Repeat("a", domain.MaxCityLength+1)},
		"q":      {Limit: 10, Query: strings.Repeat("b", domain.MaxQueryLength+1)},
	}
	for field, f := range cases {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			ve, ok := domain.AsValidationError(f.Validate())
			if !ok {
				t.Fatalf("filter %+v was accepted", f)
			}
			if ve.Violations[0].Field != field {
				t.Fatalf("violation field = %q, want %q", ve.Violations[0].Field, field)
			}
		})
	}
}

func TestStatusValid(t *testing.T) {
	t.Parallel()
	for _, s := range []domain.Status{domain.StatusActive, domain.StatusSuspended} {
		if !s.Valid() {
			t.Fatalf("%q reported invalid", s)
		}
	}
	if domain.Status("DELETED").Valid() {
		t.Fatal("unknown status reported valid")
	}
}
