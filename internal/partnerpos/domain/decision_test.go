package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

func behavior(mode domain.Mode) domain.Behavior {
	b := domain.DefaultBehavior()
	b.Mode = mode
	return b
}

func TestDecideAnswersNewOrdersByMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode   domain.Mode
		action domain.Action
	}{
		{domain.ModeAutoAccept, domain.ActionAccept},
		{domain.ModeAutoReject, domain.ActionReject},
		{domain.ModeManual, domain.ActionHold},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			d := domain.Decide(behavior(tc.mode), domain.StatusCreated, 0, time.Minute)
			if d.Action != tc.action {
				t.Fatalf("action = %s, want %s", d.Action, tc.action)
			}
			if tc.action == domain.ActionAccept && d.ETAMinutes == 0 {
				t.Fatal("a confirmation must carry the estimate")
			}
			if tc.action == domain.ActionReject && d.Reason == "" {
				t.Fatal("a refusal must carry the reason")
			}
		})
	}
}

// The ladder is driven by the age of the status, so a poll that arrives too
// early changes nothing and the till needs no state of its own.
func TestDecideWalksThePreparationLadder(t *testing.T) {
	t.Parallel()
	b := behavior(domain.ModeAutoAccept)
	const step = time.Minute

	ladder := map[string]string{
		domain.StatusConfirmed:  domain.StatusCooking,
		domain.StatusCooking:    domain.StatusDelivering,
		domain.StatusDelivering: domain.StatusDelivered,
	}
	for from, want := range ladder {
		if d := domain.Decide(b, from, step-time.Second, step); d.Action != domain.ActionHold {
			t.Fatalf("%s too early = %s, want HOLD", from, d.Action)
		}
		d := domain.Decide(b, from, step, step)
		if d.Action != domain.ActionAdvance || d.NextStatus != want {
			t.Fatalf("%s = %+v, want ADVANCE to %s", from, d, want)
		}
	}
}

func TestDecideLeavesTerminalOrdersAlone(t *testing.T) {
	t.Parallel()
	b := behavior(domain.ModeAutoAccept)
	for _, status := range []string{
		domain.StatusDelivered, domain.StatusRejected, domain.StatusCancelled,
	} {
		if d := domain.Decide(b, status, time.Hour, time.Minute); d.Action != domain.ActionHold {
			t.Fatalf("%s = %s, want HOLD", status, d.Action)
		}
	}
}

func TestDecideRespectsTheKitchenSwitch(t *testing.T) {
	t.Parallel()
	b := behavior(domain.ModeAutoAccept)
	b.Kitchen = false
	if d := domain.Decide(b, domain.StatusConfirmed, time.Hour, time.Minute); d.Action != domain.ActionHold {
		t.Fatalf("with the kitchen off = %s, want HOLD", d.Action)
	}
}

func TestBehaviorValidate(t *testing.T) {
	t.Parallel()
	if err := domain.DefaultBehavior().Validate(); err != nil {
		t.Fatalf("the default behaviour must be valid: %v", err)
	}
	cases := []struct {
		name  string
		b     domain.Behavior
		field string
	}{
		{
			name:  "unknown mode",
			b:     domain.Behavior{Mode: "NOPE", ETAMinutes: 10},
			field: "mode",
		},
		{
			name:  "negative estimate",
			b:     domain.Behavior{Mode: domain.ModeAutoAccept, ETAMinutes: -1},
			field: "eta_minutes",
		},
		{
			name:  "estimate above the cap",
			b:     domain.Behavior{Mode: domain.ModeAutoAccept, ETAMinutes: 241},
			field: "eta_minutes",
		},
		{
			name:  "refusal without a reason",
			b:     domain.Behavior{Mode: domain.ModeAutoReject, ETAMinutes: 10},
			field: "reject_reason",
		},
		{
			name: "reason too long",
			b: domain.Behavior{
				Mode: domain.ModeAutoAccept, ETAMinutes: 10,
				RejectReason: strings.Repeat("я", domain.MaxRejectReasonLength+1),
			},
			field: "reject_reason",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ve, ok := domain.AsValidationError(tc.b.Validate())
			if !ok {
				t.Fatal("must be rejected")
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

func TestWatchedStatusesExcludeTerminalOnes(t *testing.T) {
	t.Parallel()
	watched := domain.WatchedStatuses()
	if len(watched) != 4 {
		t.Fatalf("watched = %v, want the four actionable statuses", watched)
	}
	for _, s := range watched {
		if s == domain.StatusDelivered {
			t.Fatal("a delivered order needs no action")
		}
	}
}

// ------------------------------------------------------------------- menu

func TestDefaultMenuMatchesThePlatformSeed(t *testing.T) {
	t.Parallel()
	menu := domain.DefaultMenu()
	if err := menu.Validate(); err != nil {
		t.Fatalf("the default menu must be valid: %v", err)
	}
	for _, id := range []string{"POS-001", "POS-002", "POS-003"} {
		if _, ok := menu.Find(id); !ok {
			t.Fatalf("the seeded position %s is missing", id)
		}
	}
	categories := menu.Categories()
	if len(categories) != 2 || categories[0].Name != "Горячее" {
		t.Fatalf("categories = %+v", categories)
	}
	if len(categories[0].Items) != 2 || categories[0].Items[0].ExternalID != "POS-001" {
		t.Fatalf("items are not ordered: %+v", categories[0].Items)
	}
}

func TestMenuValidateRejectsBadPositions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		item  domain.Item
		field string
	}{
		{
			name:  "no external id",
			item:  domain.Item{Name: "Борщ", Category: "Горячее", PriceMinor: 100},
			field: "items.external_id",
		},
		{
			name:  "no name",
			item:  domain.Item{ExternalID: "P-1", Category: "Горячее", PriceMinor: 100},
			field: "items.name",
		},
		{
			name:  "zero price",
			item:  domain.Item{ExternalID: "P-1", Name: "Борщ", Category: "Горячее"},
			field: "items.price_minor",
		},
		{
			name: "negative quantity",
			item: domain.Item{
				ExternalID: "P-1", Name: "Борщ", Category: "Горячее",
				PriceMinor: 100, OnHandQty: -1,
			},
			field: "items.on_hand_qty",
		},
		{
			name:  "no category",
			item:  domain.Item{ExternalID: "P-1", Name: "Борщ", PriceMinor: 100},
			field: "items.category",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ve, ok := domain.AsValidationError(domain.Menu{Items: []domain.Item{tc.item}}.Validate())
			if !ok {
				t.Fatal("must be rejected")
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

func TestMenuValidateRejectsDuplicateExternalID(t *testing.T) {
	t.Parallel()
	menu := domain.Menu{Items: []domain.Item{
		{ExternalID: "P-1", Name: "Борщ", Category: "Горячее", PriceMinor: 100},
		{ExternalID: "P-1", Name: "Морс", Category: "Напитки", PriceMinor: 100},
	}}
	if _, ok := domain.AsValidationError(menu.Validate()); !ok {
		t.Fatal("a duplicate external id must be rejected")
	}
}

func TestMenuApplyAndClone(t *testing.T) {
	t.Parallel()
	menu := domain.DefaultMenu()
	price := int64(50000)
	stopped := true
	item, err := menu.Apply("POS-001", domain.ItemPatch{PriceMinor: &price, SalesStopped: &stopped})
	if err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if item.PriceMinor != price || !item.SalesStopped {
		t.Fatalf("item = %+v", item)
	}
	if _, err := menu.Apply("NOPE", domain.ItemPatch{PriceMinor: &price}); err == nil {
		t.Fatal("an unknown position must be reported")
	}

	// A copy must not share the backing array with the till menu.
	clone := menu.Clone()
	clone.Items[0].Name = "изменено"
	if menu.Items[0].Name == "изменено" {
		t.Fatal("Clone must not alias the original")
	}
}

func TestItemPatchValidate(t *testing.T) {
	t.Parallel()
	if _, ok := domain.AsValidationError((domain.ItemPatch{}).Validate()); !ok {
		t.Fatal("an empty patch must be rejected")
	}
	bad := int64(0)
	if _, ok := domain.AsValidationError((domain.ItemPatch{PriceMinor: &bad}).Validate()); !ok {
		t.Fatal("a zero price must be rejected")
	}
	good := int64(1000)
	if err := (domain.ItemPatch{PriceMinor: &good}).Validate(); err != nil {
		t.Fatalf("a valid patch was rejected: %v", err)
	}
}
