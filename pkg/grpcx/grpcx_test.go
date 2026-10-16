package grpcx_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	merchantv1 "github.com/avito-kitchen/platform/gen/grpc/merchant/v1"
	"github.com/avito-kitchen/platform/pkg/grpcx"
	"github.com/avito-kitchen/platform/pkg/logx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// stubMerchants is a minimal service behind the real server: it reports what
// the interceptors gave it.
type stubMerchants struct {
	merchantv1.UnimplementedMerchantServiceServer
	sawDeadline atomic.Bool
}

// AuthenticateApiKey echoes the correlation id the server saw.
func (s *stubMerchants) AuthenticateApiKey(ctx context.Context,
	_ *merchantv1.AuthenticateApiKeyRequest) (*merchantv1.AuthenticateApiKeyResponse, error) {
	if _, ok := ctx.Deadline(); ok {
		s.sawDeadline.Store(true)
	}
	return &merchantv1.AuthenticateApiKeyResponse{MerchantId: logx.RequestID(ctx)}, nil
}

// GetMerchant panics, so the recovery interceptor has something to recover.
func (s *stubMerchants) GetMerchant(context.Context,
	*merchantv1.GetMerchantRequest) (*merchantv1.GetMerchantResponse, error) {
	panic("boom")
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// startServer runs a real server on a local port and stops it with the test.
func startServer(t *testing.T, stub *stubMerchants) (*grpcx.Server, string) {
	t.Helper()
	addr := freeAddr(t)
	srv := grpcx.NewServer(grpcx.ServerConfig{
		Addr: addr, MaxConnectionAge: time.Minute, KeepaliveTime: time.Minute,
		KeepaliveTimeout: 10 * time.Second, MaxRecvMsgBytes: 1 << 20, HandlerTimeout: time.Second,
	}, quietLogger())
	merchantv1.RegisterMerchantServiceServer(srv.Registrar(), stub)
	srv.SetServing("", true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, time.Second) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve() = %v, want nil after the context ended", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the context ended")
		}
	})
	return srv, addr
}

func dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpcx.Dial(grpcx.ClientConfig{
		Target: addr, KeepaliveTime: time.Minute, KeepaliveTimeout: 10 * time.Second,
		MaxRecvMsgBytes: 1 << 20,
	}, quietLogger())
	if err != nil {
		t.Fatalf("Dial() = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestServerAndClientCooperate(t *testing.T) {
	t.Parallel()
	stub := &stubMerchants{}
	srv, addr := startServer(t, stub)
	conn := dial(t, addr)
	health := healthpb.NewHealthClient(conn)
	merchants := merchantv1.NewMerchantServiceClient(conn)

	// The health service is registered and reports the status it was given.
	resp, err := health.Check(callCtx(t), &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health = %v, %v; want SERVING", resp, err)
	}

	// The correlation id of the caller reaches the handler, and the handler
	// runs under the deadline of the server even when the caller set none.
	noDeadline := logx.WithRequestID(context.Background(), "req-grpc-1")
	auth, err := merchants.AuthenticateApiKey(noDeadline, &merchantv1.AuthenticateApiKeyRequest{ApiKey: "k"})
	if err != nil {
		t.Fatalf("AuthenticateApiKey() = %v", err)
	}
	if auth.GetMerchantId() != "req-grpc-1" {
		t.Fatalf("the handler saw request id %q, want req-grpc-1", auth.GetMerchantId())
	}
	if !stub.sawDeadline.Load() {
		t.Fatal("the handler ran without the deadline of the server")
	}

	// Without an incoming id the server generates one.
	generated, err := merchants.AuthenticateApiKey(callCtx(t), &merchantv1.AuthenticateApiKeyRequest{})
	if err != nil || generated.GetMerchantId() == "" {
		t.Fatalf("generated id = %q, %v", generated.GetMerchantId(), err)
	}

	// A panic becomes Internal and the server keeps serving.
	_, err = merchants.GetMerchant(callCtx(t), &merchantv1.GetMerchantRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("panicking handler = %v, want Internal", err)
	}
	if _, err := merchants.AuthenticateApiKey(callCtx(t), &merchantv1.AuthenticateApiKeyRequest{}); err != nil {
		t.Fatalf("the server stopped serving after a panic: %v", err)
	}

	srv.SetServing("", false)
	resp, err = health.Check(callCtx(t), &healthpb.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("health after SetServing(false) = %v, %v; want NOT_SERVING", resp, err)
	}
}

func TestDialRefusesAnEmptyTarget(t *testing.T) {
	t.Parallel()
	if _, err := grpcx.Dial(grpcx.ClientConfig{}, quietLogger()); err == nil {
		t.Fatal("Dial() with an empty target = nil error, want a refusal")
	}
}

func TestServeReportsAListenFailure(t *testing.T) {
	t.Parallel()
	srv := grpcx.NewServer(grpcx.ServerConfig{Addr: "127.0.0.1:-1"}, quietLogger())
	if err := srv.Serve(context.Background(), time.Second); err == nil {
		t.Fatal("Serve() = nil, want the listen error")
	}
}
