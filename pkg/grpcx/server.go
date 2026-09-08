// Package grpcx wraps the gRPC server lifecycle and the interceptors shared by
// all services. It contains no business rules.
package grpcx

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MetadataRequestID is the correlation key propagated over gRPC metadata.
const MetadataRequestID = "x-request-id"

// ServerConfig holds the gRPC listener settings.
type ServerConfig struct {
	Addr             string
	MaxConnectionAge time.Duration
	KeepaliveTime    time.Duration
	KeepaliveTimeout time.Duration
	MaxRecvMsgBytes  int
	HandlerTimeout   time.Duration
}

// Server bundles a gRPC server with its health service.
type Server struct {
	grpc   *grpc.Server
	health *health.Server
	addr   string
	logger *slog.Logger
}

// NewServer builds a gRPC server with correlation, logging, recovery and
// timeout interceptors and a registered health service.
// MaxConnectionAge is set so that clients periodically re-resolve the address
// and newly added instances receive traffic.
func NewServer(cfg ServerConfig, logger *slog.Logger) *Server {
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			requestIDInterceptor(),
			recoveryInterceptor(logger),
			timeoutInterceptor(cfg.HandlerTimeout),
			loggingInterceptor(logger),
		),
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgBytes),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      cfg.MaxConnectionAge,
			MaxConnectionAgeGrace: 10 * time.Second,
			Time:                  cfg.KeepaliveTime,
			Timeout:               cfg.KeepaliveTimeout,
		}),
	)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	return &Server{grpc: srv, health: hs, addr: cfg.Addr, logger: logger}
}

// Registrar exposes the underlying registrar for generated service code.
func (s *Server) Registrar() grpc.ServiceRegistrar { return s.grpc }

// SetServing marks a service name as SERVING or NOT_SERVING in the health API.
// The empty name is the overall server status.
func (s *Server) SetServing(service string, serving bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if serving {
		st = healthpb.HealthCheckResponse_SERVING
	}
	s.health.SetServingStatus(service, st)
}

// Serve listens until ctx is cancelled, then stops gracefully within grace.
func (s *Server) Serve(ctx context.Context, grace time.Duration) error {
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("grpc listener started", slog.String("addr", s.addr))
		errCh <- s.grpc.Serve(lis)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.health.Shutdown()
	s.logger.Info("grpc listener stopping", slog.String("addr", s.addr))
	stopped := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(grace):
		s.logger.Warn("grpc graceful stop timed out, forcing stop")
		s.grpc.Stop()
	}
	return nil
}

func requestIDInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (any, error) {
		id := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get(MetadataRequestID); len(vals) > 0 {
				id = vals[0]
			}
		}
		if id == "" || len(id) > 128 {
			id = uuid.NewString()
		}
		return handler(logx.WithRequestID(ctx, id), req)
	}
}

func recoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				logx.From(ctx, logger).Error("panic in grpc handler",
					slog.Any("panic", rec), slog.String("method", info.FullMethod))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

func timeoutInterceptor(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (any, error) {
		if d <= 0 {
			return handler(ctx, req)
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return handler(ctx, req)
	}
}

func loggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		attrs := []any{
			slog.String("grpc_method", info.FullMethod),
			slog.String("grpc_code", code.String()),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		}
		l := logx.From(ctx, logger)
		switch {
		case code == codes.OK:
			l.Info("grpc request", attrs...)
		case code == codes.Internal || code == codes.Unknown:
			l.Error("grpc request failed", append(attrs, slog.Any("error", err))...)
		default:
			l.Warn("grpc request rejected", attrs...)
		}
		return resp, err
	}
}
