package grpcclient_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	merchantv1 "github.com/avito-kitchen/platform/gen/grpc/merchant/v1"
	"github.com/avito-kitchen/platform/internal/inventory/adapters/grpcclient"
	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// The stub stands in for merchant-service, but the transport is real: the
// status codes travel through gRPC encoding exactly as in the stack.
type merchantServer struct {
	merchantv1.UnimplementedMerchantServiceServer
	resp  *merchantv1.AuthenticateApiKeyResponse
	err   error
	block bool
}

// AuthenticateApiKey answers with the configured response, error or silence.
func (s *merchantServer) AuthenticateApiKey(ctx context.Context,
	_ *merchantv1.AuthenticateApiKeyRequest) (*merchantv1.AuthenticateApiKeyResponse, error) {
	if s.block {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func directory(t *testing.T, stub *merchantServer, timeout time.Duration) *grpcclient.MerchantDirectory {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	merchantv1.RegisterMerchantServiceServer(srv, stub)
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
	return grpcclient.NewMerchantDirectory(conn, timeout)
}

func TestAuthenticateResolvesTheKey(t *testing.T) {
	t.Parallel()
	merchantID := uuid.New()
	cases := map[merchantv1.MerchantStatus]bool{
		merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE:    true,
		merchantv1.MerchantStatus_MERCHANT_STATUS_SUSPENDED: false,
	}
	for st, active := range cases {
		stub := &merchantServer{resp: &merchantv1.AuthenticateApiKeyResponse{
			MerchantId: merchantID.String(), Status: st,
		}}
		identity, err := directory(t, stub, time.Second).Authenticate(context.Background(), "key")
		if err != nil {
			t.Fatalf("%s: Authenticate() = %v", st, err)
		}
		if identity.MerchantID != merchantID || identity.Active != active {
			t.Fatalf("%s: identity = %+v, want active=%v", st, identity, active)
		}
	}
}

// An unknown key is the caller's problem (401); an unreachable directory is
// ours (503). Mixing them up would tell a merchant its key is wrong during an
// outage.
func TestAuthenticateKeepsRefusalAndOutageApart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want error
	}{
		{name: "unknown key", err: status.Error(codes.NotFound, "no such key"), want: app.ErrMerchantUnauthenticated},
		{name: "malformed key", err: status.Error(codes.InvalidArgument, "bad key"), want: app.ErrMerchantUnauthenticated},
		{name: "outage", err: status.Error(codes.Unavailable, "down"), want: app.ErrMerchantDirectoryUnavailable},
		{name: "internal", err: status.Error(codes.Internal, "boom"), want: app.ErrMerchantDirectoryUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := directory(t, &merchantServer{err: tc.err}, time.Second).
				Authenticate(context.Background(), "key")
			if !errors.Is(err, tc.want) {
				t.Fatalf("Authenticate() = %v, want %v", err, tc.want)
			}
		})
	}
}

// A slow directory is cut off by the deadline and reported as an outage.
func TestAuthenticateIsBoundedByItsDeadline(t *testing.T) {
	t.Parallel()
	start := time.Now()
	_, err := directory(t, &merchantServer{block: true}, 50*time.Millisecond).
		Authenticate(context.Background(), "key")
	if !errors.Is(err, app.ErrMerchantDirectoryUnavailable) {
		t.Fatalf("Authenticate() = %v, want ErrMerchantDirectoryUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the call took %s, want it bounded by the 50ms deadline", elapsed)
	}
}

// A caller that walked away is not an incident of the directory.
func TestAuthenticateReportsTheCallersCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	stub := &merchantServer{block: true}
	dir := directory(t, stub, 5*time.Second)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := dir.Authenticate(ctx, "key")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Authenticate() = %v, want context.Canceled", err)
	}
	if errors.Is(err, app.ErrMerchantDirectoryUnavailable) {
		t.Fatal("a cancelled request must not be reported as an outage")
	}
}

func TestAuthenticateRejectsAMalformedAnswer(t *testing.T) {
	t.Parallel()
	stub := &merchantServer{resp: &merchantv1.AuthenticateApiKeyResponse{MerchantId: "not-a-uuid"}}
	_, err := directory(t, stub, time.Second).Authenticate(context.Background(), "key")
	if !errors.Is(err, app.ErrMerchantDirectoryUnavailable) {
		t.Fatalf("Authenticate() = %v, want ErrMerchantDirectoryUnavailable", err)
	}
}
