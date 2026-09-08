// Package config loads and validates the merchant-service configuration from
// the environment. All values are validated at start-up: the process refuses
// to open its listeners with an invalid configuration.
package config

import (
	"time"

	"github.com/avito-kitchen/platform/pkg/envcfg"
)

// Config is the complete configuration of merchant-service.
type Config struct {
	ServiceName string
	LogLevel    string

	HTTPAddr string
	GRPCAddr string
	OpsAddr  string

	DatabaseURL      string
	DBMaxConns       int32
	DBMinConns       int32
	DBConnectTimeout time.Duration
	DBStatementMax   time.Duration
	DBMaxConnLife    time.Duration
	DBMaxConnIdle    time.Duration

	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	HTTPHandlerTimeout    time.Duration
	HTTPMaxBodyBytes      int64

	GRPCHandlerTimeout   time.Duration
	GRPCMaxConnectionAge time.Duration
	GRPCKeepaliveTime    time.Duration
	GRPCKeepaliveTimeout time.Duration
	GRPCMaxRecvMsgBytes  int

	ShutdownTimeout  time.Duration
	ReadinessTimeout time.Duration

	MaxMerchantBatch int
	RunMigrations    bool
}

// Load reads the configuration and reports every problem at once.
func Load() (Config, error) {
	l := envcfg.New()
	cfg := Config{
		ServiceName: l.String("SERVICE_NAME", "merchant-service"),
		LogLevel:    l.String("LOG_LEVEL", "info"),

		HTTPAddr: l.String("HTTP_ADDR", ":8080"),
		GRPCAddr: l.String("GRPC_ADDR", ":9090"),
		OpsAddr:  l.String("OPS_ADDR", ":9091"),

		DatabaseURL:      l.Required("DATABASE_URL"),
		DBMaxConns:       int32(l.PositiveInt("DB_MAX_CONNS", 10)),
		DBMinConns:       int32(l.Int("DB_MIN_CONNS", 2)),
		DBConnectTimeout: l.Duration("DB_CONNECT_TIMEOUT", 5*time.Second),
		DBStatementMax:   l.Duration("DB_STATEMENT_TIMEOUT", 3*time.Second),
		DBMaxConnLife:    l.Duration("DB_MAX_CONN_LIFETIME", 30*time.Minute),
		DBMaxConnIdle:    l.Duration("DB_MAX_CONN_IDLE_TIME", 5*time.Minute),

		HTTPReadHeaderTimeout: l.Duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		HTTPReadTimeout:       l.Duration("HTTP_READ_TIMEOUT", 15*time.Second),
		HTTPWriteTimeout:      l.Duration("HTTP_WRITE_TIMEOUT", 20*time.Second),
		HTTPIdleTimeout:       l.Duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		HTTPHandlerTimeout:    l.Duration("HTTP_HANDLER_TIMEOUT", 10*time.Second),
		HTTPMaxBodyBytes:      int64(l.PositiveInt("HTTP_MAX_BODY_BYTES", 64*1024)),

		GRPCHandlerTimeout:   l.Duration("GRPC_HANDLER_TIMEOUT", 5*time.Second),
		GRPCMaxConnectionAge: l.Duration("GRPC_MAX_CONNECTION_AGE", 30*time.Minute),
		GRPCKeepaliveTime:    l.Duration("GRPC_KEEPALIVE_TIME", 30*time.Second),
		GRPCKeepaliveTimeout: l.Duration("GRPC_KEEPALIVE_TIMEOUT", 10*time.Second),
		GRPCMaxRecvMsgBytes:  l.PositiveInt("GRPC_MAX_RECV_MSG_BYTES", 4*1024*1024),

		ShutdownTimeout:  l.Duration("SHUTDOWN_TIMEOUT", 25*time.Second),
		ReadinessTimeout: l.Duration("READINESS_TIMEOUT", 2*time.Second),

		MaxMerchantBatch: l.PositiveInt("MAX_MERCHANT_BATCH", 100),
		RunMigrations:    l.Bool("RUN_MIGRATIONS", false),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
