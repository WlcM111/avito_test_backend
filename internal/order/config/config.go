// Package config loads and validates the order-service configuration from the
// environment. All values are validated at start-up: the process refuses to
// open its listeners with an invalid configuration.
package config

import (
	"fmt"
	"time"

	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/pkg/envcfg"
)

// Config is the complete configuration of order-service.
//
// The service exposes no gRPC server: nothing in the system calls it. It is
// the only service that opens outbound connections to all three others.
type Config struct {
	ServiceName string
	LogLevel    string

	HTTPAddr string
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

	GRPCKeepaliveTime    time.Duration
	GRPCKeepaliveTimeout time.Duration
	GRPCMaxRecvMsgBytes  int

	MerchantGRPCTarget  string
	CatalogGRPCTarget   string
	InventoryGRPCTarget string

	MerchantAuthTimeout      time.Duration
	MerchantProfileTimeout   time.Duration
	CatalogQuoteTimeout      time.Duration
	InventoryReadTimeout     time.Duration
	InventoryReserveTimeout  time.Duration
	InventoryDecisionTimeout time.Duration

	ReservationTTL      time.Duration
	MaxDeliveryAttempts int32

	OutboxInterval time.Duration
	OutboxBatch    int
	OutboxLease    time.Duration
	OutboxTimeout  time.Duration

	ShutdownTimeout  time.Duration
	ReadinessTimeout time.Duration

	RunMigrations bool
}

// Load reads the configuration and reports every problem at once.
func Load() (Config, error) {
	l := envcfg.New()
	cfg := Config{
		ServiceName: l.String("SERVICE_NAME", "order-service"),
		LogLevel:    l.String("LOG_LEVEL", "info"),

		HTTPAddr: l.String("HTTP_ADDR", ":8080"),
		OpsAddr:  l.String("OPS_ADDR", ":9091"),

		DatabaseURL:      l.Required("DATABASE_URL"),
		DBMaxConns:       int32(l.PositiveInt("DB_MAX_CONNS", 10)),
		DBMinConns:       int32(l.Int("DB_MIN_CONNS", 2)),
		DBConnectTimeout: l.Duration("DB_CONNECT_TIMEOUT", 5*time.Second),
		DBStatementMax:   l.Duration("DB_STATEMENT_TIMEOUT", 5*time.Second),
		DBMaxConnLife:    l.Duration("DB_MAX_CONN_LIFETIME", 30*time.Minute),
		DBMaxConnIdle:    l.Duration("DB_MAX_CONN_IDLE_TIME", 5*time.Minute),

		HTTPReadHeaderTimeout: l.Duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		HTTPReadTimeout:       l.Duration("HTTP_READ_TIMEOUT", 15*time.Second),
		HTTPWriteTimeout:      l.Duration("HTTP_WRITE_TIMEOUT", 20*time.Second),
		HTTPIdleTimeout:       l.Duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		HTTPHandlerTimeout:    l.Duration("HTTP_HANDLER_TIMEOUT", 10*time.Second),
		HTTPMaxBodyBytes:      int64(l.PositiveInt("HTTP_MAX_BODY_BYTES", 64*1024)),

		GRPCKeepaliveTime:    l.Duration("GRPC_KEEPALIVE_TIME", 30*time.Second),
		GRPCKeepaliveTimeout: l.Duration("GRPC_KEEPALIVE_TIMEOUT", 10*time.Second),
		GRPCMaxRecvMsgBytes:  l.PositiveInt("GRPC_MAX_RECV_MSG_BYTES", 4*1024*1024),

		MerchantGRPCTarget:  l.String("MERCHANT_GRPC_TARGET", "dns:///merchant-service:9090"),
		CatalogGRPCTarget:   l.String("CATALOG_GRPC_TARGET", "dns:///catalog-service:9090"),
		InventoryGRPCTarget: l.String("INVENTORY_GRPC_TARGET", "dns:///inventory-service:9090"),

		// The deadlines follow §7.3 of the specification.
		MerchantAuthTimeout:      l.Duration("MERCHANT_CALL_TIMEOUT", 300*time.Millisecond),
		MerchantProfileTimeout:   l.Duration("MERCHANT_PROFILE_TIMEOUT", 500*time.Millisecond),
		CatalogQuoteTimeout:      l.Duration("CATALOG_QUOTE_TIMEOUT", 500*time.Millisecond),
		InventoryReadTimeout:     l.Duration("INVENTORY_READ_TIMEOUT", 500*time.Millisecond),
		InventoryReserveTimeout:  l.Duration("INVENTORY_RESERVE_TIMEOUT", 800*time.Millisecond),
		InventoryDecisionTimeout: l.Duration("INVENTORY_DECISION_TIMEOUT", 3*time.Second),

		ReservationTTL:      l.Duration("RESERVATION_TTL", 30*time.Minute),
		MaxDeliveryAttempts: int32(l.PositiveInt("OUTBOX_MAX_ATTEMPTS", domain.MaxDeliveryAttempts)),

		OutboxInterval: l.Duration("OUTBOX_INTERVAL", time.Second),
		OutboxBatch:    l.PositiveInt("OUTBOX_BATCH", 50),
		OutboxLease:    l.Duration("OUTBOX_LEASE", time.Minute),
		OutboxTimeout:  l.Duration("OUTBOX_TIMEOUT", 30*time.Second),

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
	// The retry window of the outbox has to stay inside the reservation TTL:
	// beyond it the units are swept back and a late confirmation can no longer
	// be applied.
	if window := retryWindow(cfg.MaxDeliveryAttempts); window >= cfg.ReservationTTL {
		return Config{}, fmt.Errorf(
			"invalid configuration: the outbox retry window (%s) must stay below RESERVATION_TTL (%s)",
			window, cfg.ReservationTTL)
	}
	if cfg.OutboxLease < cfg.InventoryDecisionTimeout {
		return Config{}, fmt.Errorf(
			"invalid configuration: OUTBOX_LEASE (%s) must not be shorter than INVENTORY_DECISION_TIMEOUT (%s)",
			cfg.OutboxLease, cfg.InventoryDecisionTimeout)
	}
	return cfg, nil
}

// retryWindow is the total time the dispatcher may spend on one message.
func retryWindow(attempts int32) time.Duration {
	var total time.Duration
	for i := int32(1); i <= attempts; i++ {
		total += domain.BackoffFor(i)
	}
	return total
}
