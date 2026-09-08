package grpcapi_test

import (
	"context"
	"net"
	"testing"
	"time"

	merchantv1 "github.com/avito-kitchen/platform/gen/grpc/merchant/v1"
	"github.com/avito-kitchen/platform/internal/merchant/adapters/grpcapi"
	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/avito-kitchen/platform/internal/merchant/testsupport"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newClient starts the real gRPC server over an in-memory listener, so the
// contract is exercised through actual encoding and status propagation rather
// than through a mocked client.
func newClient(t *testing.T) merchantv1.MerchantServiceClient {
	t.Helper()
	active := testsupport.SampleMerchant()
	suspended := testsupport.SuspendedMerchant()
	svc := app.NewService(
		testsupport.NewMerchantRepo(active, suspended),
		testsupport.NewAPIKeyRepo(map[string]domain.Merchant{testsupport.DevKey: active}),
		2,
	)

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	grpcapi.NewServer(svc).Register(srv)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return merchantv1.NewMerchantServiceClient(conn)
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAuthenticateApiKey(t *testing.T) {
	t.Parallel()
	client := newClient(t)
	ctx := ctxWithTimeout(t)

	resp, err := client.AuthenticateApiKey(ctx,
		&merchantv1.AuthenticateApiKeyRequest{ApiKey: testsupport.DevKey})
	if err != nil {
		t.Fatalf("AuthenticateApiKey() = %v", err)
	}
	if resp.GetMerchantId() != testsupport.SampleMerchant().ID.String() {
		t.Fatalf("merchant_id = %q", resp.GetMerchantId())
	}
	if resp.GetStatus() != merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE {
		t.Fatalf("status = %v", resp.GetStatus())
	}
}

func TestAuthenticateApiKeyErrors(t *testing.T) {
	t.Parallel()
	client := newClient(t)
	ctx := ctxWithTimeout(t)

	if _, err := client.AuthenticateApiKey(ctx,
		&merchantv1.AuthenticateApiKeyRequest{ApiKey: ""}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty key: code = %v, want InvalidArgument", status.Code(err))
	}
	if _, err := client.AuthenticateApiKey(ctx,
		&merchantv1.AuthenticateApiKeyRequest{ApiKey: "dev-merchant-key-unknown-000000"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown key: code = %v, want NotFound", status.Code(err))
	}
}

func TestGetMerchant(t *testing.T) {
	t.Parallel()
	client := newClient(t)
	ctx := ctxWithTimeout(t)
	want := testsupport.SampleMerchant()

	resp, err := client.GetMerchant(ctx, &merchantv1.GetMerchantRequest{MerchantId: want.ID.String()})
	if err != nil {
		t.Fatalf("GetMerchant() = %v", err)
	}
	got := resp.GetMerchant()
	if got.GetId() != want.ID.String() || got.GetMinOrderMinor() != want.MinOrderMinor ||
		got.GetDeliveryFeeMinor() != want.DeliveryFeeMinor || got.GetAvgPrepMinutes() != want.AvgPrepMinutes {
		t.Fatalf("merchant = %+v", got)
	}
	if got.GetVersion() != want.Version {
		t.Fatalf("version = %d, want %d", got.GetVersion(), want.Version)
	}
}

// Internal consumers must see suspended merchants: order-service needs the
// status to reject new orders with a precise reason.
func TestGetMerchantReturnsSuspended(t *testing.T) {
	t.Parallel()
	client := newClient(t)
	ctx := ctxWithTimeout(t)
	suspended := testsupport.SuspendedMerchant()

	resp, err := client.GetMerchant(ctx, &merchantv1.GetMerchantRequest{MerchantId: suspended.ID.String()})
	if err != nil {
		t.Fatalf("GetMerchant() = %v", err)
	}
	if resp.GetMerchant().GetStatus() != merchantv1.MerchantStatus_MERCHANT_STATUS_SUSPENDED {
		t.Fatalf("status = %v, want SUSPENDED", resp.GetMerchant().GetStatus())
	}
}

func TestGetMerchantErrors(t *testing.T) {
	t.Parallel()
	client := newClient(t)
	ctx := ctxWithTimeout(t)

	if _, err := client.GetMerchant(ctx,
		&merchantv1.GetMerchantRequest{MerchantId: "not-a-uuid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
	if _, err := client.GetMerchant(ctx,
		&merchantv1.GetMerchantRequest{MerchantId: uuid.NewString()}); status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGetMerchants(t *testing.T) {
	t.Parallel()
	client := newClient(t)
	ctx := ctxWithTimeout(t)
	known := testsupport.SampleMerchant()

	resp, err := client.GetMerchants(ctx, &merchantv1.GetMerchantsRequest{
		MerchantIds: []string{known.ID.String(), uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("GetMerchants() = %v", err)
	}
	if len(resp.GetMerchants()) != 1 {
		t.Fatalf("got %d merchants, want 1 (unknown ids are omitted)", len(resp.GetMerchants()))
	}

	if _, err := client.GetMerchants(ctx, &merchantv1.GetMerchantsRequest{
		MerchantIds: []string{uuid.NewString(), uuid.NewString(), uuid.NewString()},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized batch: code = %v, want InvalidArgument", status.Code(err))
	}

	empty, err := client.GetMerchants(ctx, &merchantv1.GetMerchantsRequest{})
	if err != nil {
		t.Fatalf("GetMerchants(empty) = %v", err)
	}
	if len(empty.GetMerchants()) != 0 {
		t.Fatalf("got %d merchants, want 0", len(empty.GetMerchants()))
	}
}
