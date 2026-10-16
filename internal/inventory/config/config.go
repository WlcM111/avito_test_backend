// Package config loads and validates the inventory-service configuration from
// the environment. All values are validated at start-up: the process refuses
// to open its listeners with an invalid configuration.
package config

import (
	"fmt"
	"time"

	"github.com/avito-kitchen/platform/pkg/envcfg"
)

// Config is the complete configuration of inventory-service.
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

	MerchantGRPCTarget    string
	MerchantCallTimeout   time.Duration
	MerchantKeepaliveTime time.Duration

	DefaultReservationTTL time.Duration
	MaxReservationTTL     time.Duration
	MaxAvailabilityBatch  int

	SweeperInterval time.Duration
	SweeperBatch    int
	SweeperTimeout  time.Duration

	ShutdownTimeout  time.Duration
	ReadinessTimeout time.Duration

	RunMigrations bool
}

// Load reads the configuration and reports every problem at once.
func Load() (Config, error) {
	l := envcfg.New()
	cfg := Config{
		ServiceName: l.String("SERVICE_NAME", "inventory-service"),
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

		MerchantGRPCTarget:    l.String("MERCHANT_GRPC_TARGET", "dns:///merchant-service:9090"),
		MerchantCallTimeout:   l.Duration("MERCHANT_CALL_TIMEOUT", 300*time.Millisecond),
		MerchantKeepaliveTime: l.Duration("MERCHANT_KEEPALIVE_TIME", 30*time.Second),

		DefaultReservationTTL: l.Duration("RESERVATION_DEFAULT_TTL", 30*time.Minute),
		MaxReservationTTL:     l.Duration("RESERVATION_MAX_TTL", 2*time.Hour),
		MaxAvailabilityBatch:  l.PositiveInt("MAX_AVAILABILITY_BATCH", 100),

		SweeperInterval: l.Duration("SWEEPER_INTERVAL", time.Minute),
		SweeperBatch:    l.PositiveInt("SWEEPER_BATCH", 100),
		SweeperTimeout:  l.Duration("SWEEPER_TIMEOUT", 30*time.Second),

		ShutdownTimeout:  l.Duration("SHUTDOWN_TIMEOUT", 25*time.Second),
		ReadinessTimeout: l.Duration("READINESS_TIMEOUT", 2*time.Second),

		RunMigrations: l.Bool("RUN_MIGRATIONS", false),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	if cfg.DBMinConns > cfg.DBMaxConns {
		return Config{}, fmt.Errorf(
			"invalid configuration: DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)",
			cfg.DBMinConns, cfg.DBMaxConns)
	}
	if cfg.MaxReservationTTL < cfg.DefaultReservationTTL {
		return Config{}, fmt.Errorf(
			"invalid configuration: RESERVATION_MAX_TTL (%s) must not be shorter than RESERVATION_DEFAULT_TTL (%s)",
			cfg.MaxReservationTTL, cfg.DefaultReservationTTL)
	}
	return cfg, nil
}
