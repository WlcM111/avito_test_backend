package domain_test

import (
	"strings"
	"testing"

	"github.com/avito-kitchen/platform/internal/catalog/domain"
)

func ptr[T any](v T) *T { return &v }

func TestItemPatchValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		patch   domain.ItemPatch
		wantErr bool
		field   string
	}{
		{name: "price only", patch: domain.ItemPatch{PriceMinor: ptr[int64](1000)}},
		{name: "name only", patch: domain.ItemPatch{Name: ptr("Борщ")}},
		{name: "empty patch", patch: domain.ItemPatch{}, wantErr: true, field: "body"},
		{
			name:    "zero price",
			patch:   domain.ItemPatch{PriceMinor: ptr[int64](0)},
			wantErr: true, field: "price_minor",
		},
		{
			name:    "negative price",
			patch:   domain.ItemPatch{PriceMinor: ptr[int64](-1)},
			wantErr: true, field: "price_minor",
		},
		{
			name:    "price above the cap",
			patch:   domain.ItemPatch{PriceMinor: ptr[int64](domain.MaxPriceMinor + 1)},
			wantErr: true, field: "price_minor",
		},
		{
			name:    "empty name",
			patch:   domain.ItemPatch{Name: ptr("")},
			wantErr: true, field: "name",
		},
		{
			name:    "name too long",
			patch:   domain.ItemPatch{Name: ptr(strings.Repeat("я", domain.MaxItemNameLength+1))},
			wantErr: true, field: "name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.patch.Validate()
			if tc.wantErr == (err == nil) {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				return
			}
			ve, ok := domain.AsValidationError(err)
			if !ok {
				t.Fatalf("error %v is not a validation error", err)
			}
			if !hasField(ve, tc.field) {
				t.Fatalf("violations %+v do not mention %q", ve.Violations, tc.field)
			}
		})
	}
}

// A name of exactly the maximum length is counted in runes, not bytes: a
// Cyrillic menu must not be rejected for being valid UTF-8.
func TestItemPatchNameLengthIsCountedInRunes(t *testing.T) {
	t.Parallel()
	patch := domain.ItemPatch{Name: ptr(strings.Repeat("я", domain.MaxItemNameLength))}
	if err := patch.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for %d runes", err, domain.MaxItemNameLength)
	}
}

func TestItemPatchIsEmpty(t *testing.T) {
	t.Parallel()
	if !(domain.ItemPatch{}).IsEmpty() {
		t.Fatal("an empty patch must report IsEmpty")
	}
	if (domain.ItemPatch{Name: ptr("x")}).IsEmpty() {
		t.Fatal("a patch with a name must not report IsEmpty")
	}
}

// A patch trims the name the way publication does: surrounding spaces are not
// stored, and a name of spaces only is no name at all.
func TestItemPatchNormalizeTrimsTheName(t *testing.T) {
	t.Parallel()
	trimmed := domain.ItemPatch{Name: ptr("  Борщ с говядиной \t")}.Normalize()
	if *trimmed.Name != "Борщ с говядиной" {
		t.Fatalf("name = %q, want it trimmed", *trimmed.Name)
	}
	if err := trimmed.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}

	blank := domain.ItemPatch{Name: ptr("   ")}.Normalize()
	ve, ok := domain.AsValidationError(blank.Validate())
	if !ok || !hasField(ve, "name") {
		t.Fatalf("a name of spaces only = %v, want a violation of name", blank.Validate())
	}

	price := domain.ItemPatch{PriceMinor: ptr[int64](1000)}.Normalize()
	if price.Name != nil || *price.PriceMinor != 1000 {
		t.Fatalf("a patch without a name = %+v, want it unchanged", price)
	}
}

func hasField(ve *domain.ValidationError, field string) bool {
	for _, v := range ve.Violations {
		if v.Field == field {
			return true
		}
	}
	return false
}
