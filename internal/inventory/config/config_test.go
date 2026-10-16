package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/config"
)

// isolate empties the variables a test relies on, so a value exported in the
// shell of whoever runs the tests cannot change the outcome.
func isolate(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

var keys = []string{
	"SERVICE_NAME", "HTTP_ADDR", "GRPC_ADDR", "OPS_ADDR", "DATABASE_URL",
	"DB_MAX_CONNS", "DB_MIN_CONNS", "MERCHANT_GRPC_TARGET", "MERCHANT_CALL_TIMEOUT",
	"RESERVATION_DEFAULT_TTL", "RESERVATION_MAX_TTL", "SWEEPER_INTERVAL", "SWEEPER_BATCH",
}

func TestLoadAppliesTheDefaults(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://inventory_svc:pw@postgres:5432/inventory")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.ServiceName != "inventory-service" || cfg.HTTPAddr != ":8080" ||
		cfg.GRPCAddr != ":9090" || cfg.OpsAddr != ":9091" ||
		cfg.MerchantGRPCTarget != "dns:///merchant-service:9090" {
		t.Fatalf("names and addresses = %+v", cfg)
	}
	if cfg.MerchantCallTimeout != 300*time.Millisecond || cfg.DefaultReservationTTL != 30*time.Minute ||
		cfg.MaxReservationTTL != 2*time.Hour || cfg.SweeperInterval != time.Minute || cfg.SweeperBatch != 100 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadRequiresTheDatabase(t *testing.T) {
	isolate(t, keys...)
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() = %v, want DATABASE_URL reported", err)
	}
}

// A hold may never be asked to outlive the longest TTL the service accepts.
func TestLoadRefusesAMaximumTTLBelowTheDefault(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/inventory")
	t.Setenv("RESERVATION_DEFAULT_TTL", "3h")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "RESERVATION_MAX_TTL") {
		t.Fatalf("Load() = %v, want RESERVATION_MAX_TTL reported", err)
	}
}

func TestLoadRefusesMoreMinimumThanMaximumConnections(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/inventory")
	t.Setenv("DB_MIN_CONNS", "20")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS") {
		t.Fatalf("Load() = %v, want DB_MIN_CONNS reported", err)
	}
}

func TestLoadReportsAMalformedDuration(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/inventory")
	t.Setenv("SWEEPER_INTERVAL", "every minute")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "SWEEPER_INTERVAL") {
		t.Fatalf("Load() = %v, want SWEEPER_INTERVAL reported", err)
	}
}
