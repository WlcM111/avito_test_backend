// Package grpcclient is the outbound adapter of inventory-service. It talks to
// merchant-service over the frozen merchant.v1 contract and translates gRPC
// statuses into application errors. It contains no business rules.
package grpcclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	merchantv1 "github.com/avito-kitchen/platform/gen/grpc/merchant/v1"
	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MerchantDirectory resolves merchant access keys through merchant-service.
type MerchantDirectory struct {
	client  merchantv1.MerchantServiceClient
	timeout time.Duration
}

// NewMerchantDirectory wires the adapter to an existing client connection.
// timeout bounds every call so that a slow dependency cannot hold a request
// handler, and therefore a database connection, open.
func NewMerchantDirectory(conn grpc.ClientConnInterface, timeout time.Duration) *MerchantDirectory {
	if timeout <= 0 {
		timeout = 300 * time.Millisecond
	}
	return &MerchantDirectory{client: merchantv1.NewMerchantServiceClient(conn), timeout: timeout}
}

// Authenticate resolves an access key to its merchant.
//
// The two failure modes are kept apart on purpose: an unknown key is an
// authentication failure of the caller, while an unreachable directory is our
// own outage. Reporting the second as 401 would tell a merchant its key is
// invalid, which is false.
func (d *MerchantDirectory) Authenticate(ctx context.Context, apiKey string) (app.MerchantIdentity, error) {
	callCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	resp, err := d.client.AuthenticateApiKey(callCtx,
		&merchantv1.AuthenticateApiKeyRequest{ApiKey: apiKey})
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound, codes.InvalidArgument:
			return app.MerchantIdentity{}, app.ErrMerchantUnauthenticated
		case codes.Canceled:
			if errors.Is(ctx.Err(), context.Canceled) {
				return app.MerchantIdentity{}, ctx.Err()
			}
			return app.MerchantIdentity{}, fmt.Errorf("%w: %v", app.ErrMerchantDirectoryUnavailable, err)
		default:
			return app.MerchantIdentity{}, fmt.Errorf("%w: %v", app.ErrMerchantDirectoryUnavailable, err)
		}
	}

	merchantID, parseErr := uuid.Parse(resp.GetMerchantId())
	if parseErr != nil {
		return app.MerchantIdentity{}, fmt.Errorf(
			"%w: merchant directory returned an invalid id %q",
			app.ErrMerchantDirectoryUnavailable, resp.GetMerchantId())
	}
	return app.MerchantIdentity{
		MerchantID: merchantID,
		Active:     resp.GetStatus() == merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE,
	}, nil
}
