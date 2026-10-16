// Package grpcclient is the outbound adapter of order-service. It talks to
// merchant-service, catalog-service and inventory-service over their frozen
// contracts and translates gRPC statuses into application errors. It contains
// no business rules.
//
// Every call is bounded by its own deadline (TZ v3.0 §7.3). Without one a slow
// dependency would hold an HTTP handler, and with it a database connection,
// for as long as it takes that dependency to fail.
package grpcclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	merchantv1 "github.com/avito-kitchen/platform/gen/grpc/merchant/v1"
	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Names of the dependencies as they appear in logs and in 503 causes.
const (
	depMerchant  = "merchant-service"
	depCatalog   = "catalog-service"
	depInventory = "inventory-service"
)

// MerchantDirectory resolves access keys and reads commercial profiles through
// merchant-service.
type MerchantDirectory struct {
	client         merchantv1.MerchantServiceClient
	authTimeout    time.Duration
	profileTimeout time.Duration
}

// NewMerchantDirectory wires the adapter to an existing client connection.
func NewMerchantDirectory(conn grpc.ClientConnInterface,
	authTimeout, profileTimeout time.Duration) *MerchantDirectory {
	if authTimeout <= 0 {
		authTimeout = 300 * time.Millisecond
	}
	if profileTimeout <= 0 {
		profileTimeout = 500 * time.Millisecond
	}
	return &MerchantDirectory{
		client:         merchantv1.NewMerchantServiceClient(conn),
		authTimeout:    authTimeout,
		profileTimeout: profileTimeout,
	}
}

// Authenticate resolves an access key to its merchant.
//
// An unknown key and an unreachable directory are kept apart on purpose:
// answering 401 during an outage would tell a merchant its key is invalid,
// which is false.
func (d *MerchantDirectory) Authenticate(ctx context.Context, apiKey string) (app.MerchantIdentity, error) {
	callCtx, cancel := context.WithTimeout(ctx, d.authTimeout)
	defer cancel()

	resp, err := d.client.AuthenticateApiKey(callCtx,
		&merchantv1.AuthenticateApiKeyRequest{ApiKey: apiKey})
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound, codes.InvalidArgument:
			return app.MerchantIdentity{}, app.ErrMerchantUnauthenticated
		default:
			return app.MerchantIdentity{}, dependencyError(ctx, depMerchant, err)
		}
	}
	merchantID, parseErr := uuid.Parse(resp.GetMerchantId())
	if parseErr != nil {
		return app.MerchantIdentity{}, app.Unavailable(depMerchant,
			fmt.Errorf("invalid merchant id %q", resp.GetMerchantId()))
	}
	return app.MerchantIdentity{
		MerchantID: merchantID,
		Active:     resp.GetStatus() == merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE,
	}, nil
}

// GetMerchant returns the commercial profile an order is placed against.
func (d *MerchantDirectory) GetMerchant(ctx context.Context, merchantID uuid.UUID) (app.MerchantProfile, error) {
	callCtx, cancel := context.WithTimeout(ctx, d.profileTimeout)
	defer cancel()

	resp, err := d.client.GetMerchant(callCtx,
		&merchantv1.GetMerchantRequest{MerchantId: merchantID.String()})
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound:
			return app.MerchantProfile{}, app.ErrMerchantNotFound
		case codes.InvalidArgument:
			return app.MerchantProfile{}, app.ErrMerchantNotFound
		default:
			return app.MerchantProfile{}, dependencyError(ctx, depMerchant, err)
		}
	}
	m := resp.GetMerchant()
	if m == nil {
		return app.MerchantProfile{}, app.Unavailable(depMerchant,
			errors.New("merchant directory returned an empty profile"))
	}
	id, parseErr := uuid.Parse(m.GetId())
	if parseErr != nil {
		return app.MerchantProfile{}, app.Unavailable(depMerchant,
			fmt.Errorf("invalid merchant id %q", m.GetId()))
	}
	return app.MerchantProfile{
		ID:                id,
		Name:              m.GetName(),
		Active:            m.GetStatus() == merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE,
		IsAcceptingOrders: m.GetIsAcceptingOrders(),
		MinOrderMinor:     m.GetMinOrderMinor(),
		DeliveryFeeMinor:  m.GetDeliveryFeeMinor(),
		AvgPrepMinutes:    m.GetAvgPrepMinutes(),
	}, nil
}

// invalidRequest reports that a dependency refused our message as malformed.
// Retrying the same message cannot succeed, so it is kept apart from an
// outage: the transport answers it with 500, not with a retryable 503.
func invalidRequest(dependency string, err error) error {
	return fmt.Errorf("%w: %s: %w", app.ErrInvalidRequest, dependency, err)
}

// dependencyError distinguishes our own cancellation from an outage of the
// dependency: a client that walked away is not an incident.
func dependencyError(ctx context.Context, dependency string, err error) error {
	if status.Code(err) == codes.Canceled && errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return app.Unavailable(dependency, err)
}
