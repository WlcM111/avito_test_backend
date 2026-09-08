package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

var (
	merchantID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	customerID = uuid.MustParse("cccccccc-1111-4111-8111-111111111111")
	itemA      = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	itemB      = uuid.MustParse("a1111111-1111-4111-8111-222222222222")
)

func validDraft() domain.Draft {
	return domain.Draft{
		MerchantID:      merchantID,
		CustomerID:      customerID,
		Lines:           []domain.Line{{MenuItemID: itemA, Qty: 2}},
		CustomerName:    "Иван",
		CustomerPhone:   "+79990000001",
		DeliveryAddress: "Невский проспект, 1",
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

func TestDraftValidateAcceptsAWellFormedBasket(t *testing.T) {
	t.Parallel()
	if err := validDraft().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestDraftValidateReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()
	draft := domain.Draft{}
	ve, ok := domain.AsValidationError(draft.Validate())
	if !ok {
		t.Fatal("an empty draft must be rejected")
	}
	for _, field := range []string{
		"merchant_id", "X-Customer-Id", "customer_name", "customer_phone",
		"delivery_address", "items",
	} {
		if !hasField(ve, field) {
			t.Fatalf("violations %+v do not mention %q", ve.Violations, field)
		}
	}
}

func TestDraftValidateRejectsBadQuantities(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []domain.Line
		field string
	}{
		{name: "zero", lines: []domain.Line{{MenuItemID: itemA, Qty: 0}}, field: "items.qty"},
		{name: "negative", lines: []domain.Line{{MenuItemID: itemA, Qty: -1}}, field: "items.qty"},
		{name: "above the cap", lines: []domain.Line{{MenuItemID: itemA, Qty: 100}}, field: "items.qty"},
		{name: "no item id", lines: []domain.Line{{Qty: 1}}, field: "items.menu_item_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			draft := validDraft()
			draft.Lines = tc.lines
			ve, ok := domain.AsValidationError(draft.Validate())
			if !ok || !hasField(ve, tc.field) {
				t.Fatalf("violations %+v do not mention %q", ve, tc.field)
			}
		})
	}
}

// The basket cannot ask for more units than inventory-service accepts in one
// reservation, otherwise a valid order could never be held.
func TestDraftValidateRejectsTooManyUnits(t *testing.T) {
	t.Parallel()
	draft := validDraft()
	draft.Lines = nil
	for i := 0; i < 3; i++ {
		draft.Lines = append(draft.Lines, domain.Line{MenuItemID: uuid.New(), Qty: 99})
	}
	ve, ok := domain.AsValidationError(draft.Validate())
	if !ok || !hasField(ve, "items") {
		t.Fatalf("an oversized basket must be rejected, got %+v", ve)
	}
}

func TestDraftValidateRejectsTooManyPositions(t *testing.T) {
	t.Parallel()
	draft := validDraft()
	draft.Lines = nil
	for i := 0; i <= domain.MaxOrderLines; i++ {
		draft.Lines = append(draft.Lines, domain.Line{MenuItemID: uuid.New(), Qty: 1})
	}
	ve, ok := domain.AsValidationError(draft.Validate())
	if !ok || !hasField(ve, "items") {
		t.Fatalf("a basket with too many positions must be rejected, got %+v", ve)
	}
}

func TestDraftValidateRejectsAnOversizedComment(t *testing.T) {
	t.Parallel()
	draft := validDraft()
	draft.Comment = strings.Repeat("я", domain.MaxCommentLength+1)
	ve, ok := domain.AsValidationError(draft.Validate())
	if !ok || !hasField(ve, "comment") {
		t.Fatalf("an oversized comment must be rejected, got %+v", ve)
	}
}

// The same position added twice is one position with a larger quantity, not
// two lines: the reservation and the primary key of order_items both require
// it to be unique.
func TestNormalizeLinesMergesAndOrders(t *testing.T) {
	t.Parallel()
	lines := domain.NormalizeLines([]domain.Line{
		{MenuItemID: itemB, Qty: 1},
		{MenuItemID: itemA, Qty: 2},
		{MenuItemID: itemB, Qty: 3},
	})
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].MenuItemID != itemA || lines[1].MenuItemID != itemB {
		t.Fatalf("lines are not ordered by identifier: %+v", lines)
	}
	if lines[1].Qty != 4 {
		t.Fatalf("duplicate quantities are not merged: %+v", lines[1])
	}
}

func TestNormalizeLinesOfAnEmptyBasket(t *testing.T) {
	t.Parallel()
	if lines := domain.NormalizeLines(nil); lines != nil {
		t.Fatalf("NormalizeLines(nil) = %+v, want nil", lines)
	}
}

func TestPreviewOrderableRequiresEveryPositionAndNoOrderProblem(t *testing.T) {
	t.Parallel()
	good := domain.Preview{Lines: []domain.PreviewLine{
		{MenuItemID: itemA, Qty: 1, Problem: domain.ItemProblemNone},
	}}
	if !good.Orderable() {
		t.Fatal("a preview without problems must be orderable")
	}

	withLineProblem := good
	withLineProblem.Lines = []domain.PreviewLine{
		{MenuItemID: itemA, Qty: 1, Problem: domain.ItemProblemInsufficientUnits},
	}
	if withLineProblem.Orderable() {
		t.Fatal("a shortage must make the preview not orderable")
	}

	withOrderProblem := good
	withOrderProblem.Problems = []domain.OrderProblem{domain.OrderProblemBelowMinOrder}
	if withOrderProblem.Orderable() {
		t.Fatal("an order-level problem must make the preview not orderable")
	}

	empty := domain.Preview{}
	if empty.Orderable() {
		t.Fatal("an empty preview must not be orderable")
	}
}

func TestPreviewProducesTheOrderSnapshot(t *testing.T) {
	t.Parallel()
	preview := domain.Preview{Lines: []domain.PreviewLine{{
		MenuItemID: itemA, ExternalID: "POS-001", Name: "Борщ",
		UnitPriceMinor: 42000, Qty: 2, LineTotalMinor: 84000,
	}}}
	items := preview.Items()
	if len(items) != 1 || items[0].LineTotalMinor != 84000 || items[0].ExternalID != "POS-001" {
		t.Fatalf("snapshot = %+v", items)
	}
	lines := preview.ReserveLines()
	if len(lines) != 1 || lines[0].Qty != 2 || lines[0].MenuItemID != itemA {
		t.Fatalf("reserve lines = %+v", lines)
	}
}

func TestMoneyHelpers(t *testing.T) {
	t.Parallel()
	if got := domain.LineTotalMinor(42000, 3); got != 126000 {
		t.Fatalf("LineTotalMinor = %d, want 126000", got)
	}
	if got := domain.TotalMinor(126000, 19900); got != 145900 {
		t.Fatalf("TotalMinor = %d, want 145900", got)
	}
}

// The retry schedule has to grow and then stop growing, and the whole window
// must stay inside the reservation TTL: beyond it the units are already back.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	t.Parallel()
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 64 * time.Second, 128 * time.Second,
		256 * time.Second, domain.MaxBackoff, domain.MaxBackoff, domain.MaxBackoff,
	}
	var total time.Duration
	for i, expected := range want {
		got := domain.BackoffFor(int32(i + 1))
		if got != expected {
			t.Fatalf("BackoffFor(%d) = %s, want %s", i+1, got, expected)
		}
		total += got
	}
	if total >= 30*time.Minute {
		t.Fatalf("the retry window %s must stay below the 30 minute reservation TTL", total)
	}
}

func TestMessageForTransition(t *testing.T) {
	t.Parallel()
	orderID := uuid.New()

	commit, ok := domain.MessageForTransition(orderID, domain.StatusConfirmed, "req-1")
	if !ok || commit.Type != domain.MessageCommitReservation || commit.ReservationID != orderID {
		t.Fatalf("confirmation must produce a commit message, got %+v", commit)
	}
	reject, ok := domain.MessageForTransition(orderID, domain.StatusRejected, "req-1")
	if !ok || reject.Reason != domain.ReleaseOrderRejected {
		t.Fatalf("refusal must produce a release message, got %+v", reject)
	}
	cancel, ok := domain.MessageForTransition(orderID, domain.StatusCancelled, "req-1")
	if !ok || cancel.Reason != domain.ReleaseOrderCancelled {
		t.Fatalf("cancellation must produce a release message, got %+v", cancel)
	}
	// Moving a confirmed order along changes nothing outside this service.
	if _, ok := domain.MessageForTransition(orderID, domain.StatusCooking, "req-1"); ok {
		t.Fatal("progress transitions must not schedule a message")
	}
}

func TestReleaseReasonAndMessageTypeValidity(t *testing.T) {
	t.Parallel()
	if !domain.ReleaseOrderCancelled.Valid() || domain.ReleaseReason("TTL_EXPIRED").Valid() {
		t.Fatal("only the three caller reasons are valid")
	}
	if !domain.MessageCommitReservation.Valid() || domain.MessageType("NOPE").Valid() {
		t.Fatal("only the two message types are valid")
	}
}

func TestExpectedTotalIsValidated(t *testing.T) {
	t.Parallel()
	draft := validDraft()
	draft.ExpectedTotalMinor = ptr[int64](0)
	ve, ok := domain.AsValidationError(draft.Validate())
	if !ok || !hasField(ve, "expected_total_minor") {
		t.Fatalf("a non-positive expected total must be rejected, got %+v", ve)
	}
}
