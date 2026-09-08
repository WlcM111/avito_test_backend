package app

import (
	"context"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
)

// PreviewOrder recalculates a draft and reports every problem at once.
//
// It is a read: nothing is held and nothing is stored. The three dependencies
// answer different questions and none of them can answer another's:
// merchant-service says whether the outlet takes orders and on what terms,
// catalog-service says what the positions are called and cost, and
// inventory-service says how many units of each are actually available. A
// position is orderable only when all three agree.
func (s *Service) PreviewOrder(ctx context.Context, draft domain.Draft) (domain.Preview, error) {
	draft.Lines = domain.NormalizeLines(draft.Lines)
	ve := &domain.ValidationError{}
	if draft.MerchantID == uuid.Nil {
		ve.Add("merchant_id", "is required")
	}
	if err := domain.ValidateLines(draft.Lines); err != nil {
		if lineErr, ok := domain.AsValidationError(err); ok {
			ve.Violations = append(ve.Violations, lineErr.Violations...)
		}
	}
	if err := ve.OrNil(); err != nil {
		return domain.Preview{}, err
	}

	profile, err := s.merchants.GetMerchant(ctx, draft.MerchantID)
	if err != nil {
		return domain.Preview{}, err
	}

	ids := make([]uuid.UUID, 0, len(draft.Lines))
	qty := make(map[uuid.UUID]int32, len(draft.Lines))
	for _, l := range draft.Lines {
		ids = append(ids, l.MenuItemID)
		qty[l.MenuItemID] = l.Qty
	}

	quote, err := s.catalogue.QuoteItems(ctx, draft.MerchantID, ids)
	if err != nil {
		return domain.Preview{}, err
	}
	availability, err := s.stock.GetAvailability(ctx, draft.MerchantID, ids)
	if err != nil {
		return domain.Preview{}, err
	}
	stockByItem := make(map[uuid.UUID]Availability, len(availability))
	for _, a := range availability {
		stockByItem[a.MenuItemID] = a
	}

	preview := domain.Preview{
		MerchantID:       profile.ID,
		MerchantName:     profile.Name,
		MenuVersion:      quote.MenuVersion,
		DeliveryFeeMinor: profile.DeliveryFeeMinor,
		MinOrderMinor:    profile.MinOrderMinor,
		Currency:         domain.Currency,
		ETAMinutes:       profile.AvgPrepMinutes,
		Lines:            make([]domain.PreviewLine, 0, len(draft.Lines)),
	}

	quoteByItem := make(map[uuid.UUID]QuotedItem, len(quote.Items))
	for _, q := range quote.Items {
		quoteByItem[q.MenuItemID] = q
	}

	for _, l := range draft.Lines {
		preview.Lines = append(preview.Lines,
			previewLine(l, quoteByItem[l.MenuItemID], stockByItem[l.MenuItemID], quoteByItem))
	}
	for _, line := range preview.Lines {
		if line.Problem == domain.ItemProblemNone {
			preview.ItemsTotalMinor += line.LineTotalMinor
		}
	}
	preview.TotalMinor = domain.TotalMinor(preview.ItemsTotalMinor, preview.DeliveryFeeMinor)

	// Order-level problems are independent of the positions: a closed outlet
	// refuses a perfectly available basket, and a full basket can still be
	// below the minimum.
	if !profile.Active {
		preview.Problems = append(preview.Problems, domain.OrderProblemMerchantSuspended)
	}
	if !profile.IsAcceptingOrders {
		preview.Problems = append(preview.Problems, domain.OrderProblemNotAcceptingOrders)
	}
	// The minimum applies to the positions, not to the delivery fee: charging
	// the customer for delivery to reach the merchant's minimum would be a
	// different promise than the one the outlet made.
	//
	// It is judged only on a basket whose positions can all be ordered. A
	// position with a problem contributes nothing to the total, so raising the
	// minimum next to it would name a consequence instead of the cause: the
	// customer would add items and still be refused for the stop list or the
	// shortage. Once the positions are fixed, the next preview reports the
	// minimum if it is still not met.
	if !hasLineProblem(preview.Lines) && preview.ItemsTotalMinor < profile.MinOrderMinor {
		preview.Problems = append(preview.Problems, domain.OrderProblemBelowMinOrder)
	}
	return preview, nil
}

// hasLineProblem reports whether any requested position cannot be ordered.
func hasLineProblem(lines []domain.PreviewLine) bool {
	for _, line := range lines {
		if line.Problem != domain.ItemProblemNone {
			return true
		}
	}
	return false
}

// previewLine combines what the catalogue and the stock ledger say about one
// requested position. The catalogue problem wins: an unknown position has no
// price, and reporting "not enough units" for it would be misleading.
func previewLine(line domain.Line, quoted QuotedItem, stock Availability,
	quoteByItem map[uuid.UUID]QuotedItem) domain.PreviewLine {
	out := domain.PreviewLine{
		MenuItemID:   line.MenuItemID,
		Qty:          line.Qty,
		AvailableQty: stock.AvailableQty,
	}
	if _, quotedAtAll := quoteByItem[line.MenuItemID]; !quotedAtAll {
		out.Problem = domain.ItemProblemNotFound
		return out
	}
	out.ExternalID = quoted.ExternalID
	out.Name = quoted.Name
	if quoted.Problem != domain.ItemProblemNone {
		out.Problem = quoted.Problem
		return out
	}
	out.UnitPriceMinor = quoted.UnitPriceMinor
	out.LineTotalMinor = domain.LineTotalMinor(quoted.UnitPriceMinor, line.Qty)

	switch {
	case !stock.Tracked:
		out.Problem = domain.ItemProblemNotTracked
	case stock.SalesStopped:
		out.Problem = domain.ItemProblemSalesStopped
	case stock.AvailableQty < line.Qty:
		out.Problem = domain.ItemProblemInsufficientUnits
	default:
		out.Problem = domain.ItemProblemNone
	}
	return out
}
