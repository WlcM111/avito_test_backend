package grpcx

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/avito-kitchen/platform/pkg/logx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// ClientConfig describes an outbound gRPC connection.
type ClientConfig struct {
	// Target is a gRPC name, for example dns:///inventory-service:9090.
	// A dns:/// target is re-resolved periodically, so instances added later
	// receive traffic without restarting the caller.
	Target           string
	KeepaliveTime    time.Duration
	KeepaliveTimeout time.Duration
	MaxRecvMsgBytes  int
}

// Dial creates a client connection with correlation propagation and keepalive.
// Transport security is not configured: internal gRPC ports are reachable only
// inside the deployment network and are never published.
func Dial(cfg ClientConfig, logger *slog.Logger) (*grpc.ClientConn, error) {
	if cfg.Target == "" {
		return nil, fmt.Errorf("grpc target is empty")
	}
	conn, err := grpc.NewClient(cfg.Target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(propagateRequestID()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(cfg.MaxRecvMsgBytes)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cfg.KeepaliveTime,
			Timeout:             cfg.KeepaliveTimeout,
			PermitWithoutStream: false,
		}),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`),
	)
	if err != nil {
		return nil, fmt.Errorf("create grpc client for %s: %w", cfg.Target, err)
	}
	logger.Info("grpc client created", slog.String("target", cfg.Target))
	return conn, nil
}

// propagateRequestID copies the correlation id of the incoming request into
// the outgoing call, so one identifier spans the whole chain in the logs.
func propagateRequestID() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if id := logx.RequestID(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, MetadataRequestID, id)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
