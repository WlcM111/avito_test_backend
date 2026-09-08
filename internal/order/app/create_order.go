package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/google/uuid"
)

// CreateOrder places an order (TZ v3.0 §8.1).
//
// The sequence is fixed by the design: the merchant profile and the prices are
// read first, then the units are held, and only then the order is written. The
// hold before the local commit is the single window of divergence in the whole
// system; it is closed by the reservation TTL and the sweeper of
// inventory-service, and it is deliberately preferred to the alternative -
// writing the order first and discovering afterwards that the last portion was
// already sold.
func (s *Service) CreateOrder(ctx context.Context, draft domain.Draft,
	idempotencyKey string) (CreateResult, error) {
	draft.Lines = domain.NormalizeLines(draft.Lines)
	ve := &domain.ValidationError{}
	switch n := len(strings.TrimSpace(idempotencyKey)); {
	case n == 0:
		ve.Add("Idempotency-Key", "is required")
	case n > domain.MaxIdempotencyKeyLength:
		ve.Add("Idempotency-Key", "must not exceed 128 characters")
	}
	if err := draft.Validate(); err != nil {
		if draftErr, ok := domain.AsValidationError(err); ok {
			ve.Violations = append(ve.Violations, draftErr.Violations...)
		}
	}
	if err := ve.OrNil(); err != nil {
		return CreateResult{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)

	// A repeat of the same key is the common case of a double submit. Finding
	// the earlier order here avoids holding units a second time; the UNIQUE
	// constraint below remains the authority for a genuine race.
	existing, err := s.orders.FindByIdempotencyKey(ctx, draft.CustomerID, idempotencyKey)
	switch {
	case err == nil:
		return CreateResult{Order: existing, Duplicate: true}, nil
	case errors.Is(err, domain.ErrOrderNotFound):
	default:
		return CreateResult{}, err
	}

	preview, err := s.PreviewOrder(ctx, draft)
	if err != nil {
		return CreateResult{}, err
	}
	if !preview.Orderable() {
		return CreateResult{}, &domain.NotOrderableError{
			Lines: preview.Lines, Problems: preview.Problems,
		}
	}
	// The customer agreed to a total in the preview. If the merchant changed a
	// price in between, the order is refused rather than placed at the new
	// price.
	if draft.ExpectedTotalMinor != nil && *draft.ExpectedTotalMinor != preview.TotalMinor {
		return CreateResult{}, domain.ErrPriceChanged
	}

	// The reservation identifier is the order identifier (TZ §5.4), so the
	// hold and the order it belongs to can never drift apart.
	orderID := uuid.New()
	reserved, err := s.stock.Reserve(ctx, orderID, draft.MerchantID,
		preview.ReserveLines(), s.cfg.ReservationTTL)
	if err != nil {
		if rejected, ok := AsReserveRejected(err); ok {
			return CreateResult{}, notOrderableFromRejection(preview, rejected)
		}
		return CreateResult{}, err
	}

	order := domain.Order{
		ID:               orderID,
		MerchantID:       preview.MerchantID,
		MerchantName:     preview.MerchantName,
		CustomerID:       draft.CustomerID,
		IdempotencyKey:   idempotencyKey,
		CustomerName:     strings.TrimSpace(draft.CustomerName),
		CustomerPhone:    strings.TrimSpace(draft.CustomerPhone),
		DeliveryAddress:  strings.TrimSpace(draft.DeliveryAddress),
		Comment:          strings.TrimSpace(draft.Comment),
		Status:           domain.StatusCreated,
		Items:            preview.Items(),
		ItemsTotalMinor:  preview.ItemsTotalMinor,
		DeliveryFeeMinor: preview.DeliveryFeeMinor,
		TotalMinor:       preview.TotalMinor,
		Currency:         domain.Currency,
		ETAMinutes:       preview.ETAMinutes,
	}

	stored, duplicate, err := s.orders.Create(ctx, order)
	if err != nil {
		// The order was not written, so the hold we just took is useless.
		// Returning it now is better than waiting for the TTL, but the TTL
		// remains the guarantee: this call is best effort.
		s.releaseOrphanHold(ctx, orderID, "order was not stored")
		return CreateResult{}, err
	}
	if duplicate {
		// Two concurrent submits of the same key: the other one won. Our hold
		// belongs to an order that will never exist.
		s.releaseOrphanHold(ctx, orderID, "idempotency key was used concurrently")
		return CreateResult{Order: stored, Duplicate: true}, nil
	}
	logx.From(ctx, s.logger).Info("order created",
		slog.String("order_id", stored.ID.String()),
		slog.String("merchant_id", stored.MerchantID.String()),
		slog.Int64("total_minor", stored.TotalMinor),
		slog.Time("hold_expires_at", reserved.ExpiresAt),
	)
	return CreateResult{Order: stored}, nil
}

// releaseOrphanHold returns units held for an order that was not stored. It
// runs on a context detached from the request, because the request may already
// be cancelled and the units still have to come back.
func (s *Service) releaseOrphanHold(ctx context.Context, reservationID uuid.UUID, why string) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.stock.Release(releaseCtx, reservationID, domain.ReleaseOrderExpired); err != nil {
		// Not fatal: the sweeper of inventory-service releases the hold when
		// its TTL passes.
		logx.From(ctx, s.logger).Warn("orphan hold not released, leaving it to the TTL",
			slog.String("reservation_id", reservationID.String()),
			slog.String("reason", why), slog.Any("error", err))
		return
	}
	logx.From(ctx, s.logger).Info("orphan hold released",
		slog.String("reservation_id", reservationID.String()), slog.String("reason", why))
}

// notOrderableFromRejection turns a refusal of inventory-service into the same
// answer the preview would have produced, so the client handles one shape of
// error regardless of where the shortage was noticed.
func notOrderableFromRejection(preview domain.Preview,
	rejected *ReserveRejectedError) *domain.NotOrderableError {
	byItem := make(map[uuid.UUID]RejectedLine, len(rejected.Lines))
	for _, l := range rejected.Lines {
		byItem[l.MenuItemID] = l
	}
	lines := make([]domain.PreviewLine, 0, len(preview.Lines))
	for _, line := range preview.Lines {
		if rejectedLine, ok := byItem[line.MenuItemID]; ok {
			line.Problem = rejectedLine.Problem
			line.AvailableQty = rejectedLine.AvailableQty
		}
		lines = append(lines, line)
	}
	return &domain.NotOrderableError{Lines: lines}
}
