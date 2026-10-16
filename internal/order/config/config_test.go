package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/order/config"
	"github.com/avito-kitchen/platform/internal/order/domain"
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
	"SERVICE_NAME", "HTTP_ADDR", "OPS_ADDR", "DATABASE_URL", "DB_MAX_CONNS", "DB_MIN_CONNS",
	"MERCHANT_GRPC_TARGET", "CATALOG_GRPC_TARGET", "INVENTORY_GRPC_TARGET",
	"INVENTORY_DECISION_TIMEOUT", "RESERVATION_TTL", "OUTBOX_MAX_ATTEMPTS",
	"OUTBOX_INTERVAL", "OUTBOX_BATCH", "OUTBOX_LEASE",
}

func TestLoadAppliesTheDefaults(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://orders_svc:pw@postgres:5432/orders")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.ServiceName != "order-service" || cfg.HTTPAddr != ":8080" || cfg.OpsAddr != ":9091" ||
		cfg.MerchantGRPCTarget != "dns:///merchant-service:9090" ||
		cfg.CatalogGRPCTarget != "dns:///catalog-service:9090" ||
		cfg.InventoryGRPCTarget != "dns:///inventory-service:9090" {
		t.Fatalf("names and addresses = %+v", cfg)
	}
	if cfg.ReservationTTL != 30*time.Minute || cfg.MaxDeliveryAttempts != domain.MaxDeliveryAttempts ||
		cfg.OutboxInterval != time.Second || cfg.OutboxBatch != 50 || cfg.OutboxLease != time.Minute ||
		cfg.InventoryDecisionTimeout != 3*time.Second {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadRequiresTheDatabase(t *testing.T) {
	isolate(t, keys...)
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() = %v, want DATABASE_URL reported", err)
	}
}

// The outbox must finish its retries while the hold still exists: a
// confirmation delivered after the sweeper returned the units cannot apply.
func TestLoadRefusesARetryWindowBeyondTheReservationTTL(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/orders")
	t.Setenv("RESERVATION_TTL", "10m")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "RESERVATION_TTL") {
		t.Fatalf("Load() = %v, want the retry window refused", err)
	}

	// Fewer attempts shorten the window enough for a short TTL.
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "3")
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load() with 3 attempts and a 10m TTL = %v, want it accepted", err)
	}
}

// A lease shorter than the decision call would let a second dispatcher pick
// up a message that is still being delivered.
func TestLoadRefusesALeaseShorterThanTheDecisionCall(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/orders")
	t.Setenv("OUTBOX_LEASE", "1s")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "OUTBOX_LEASE") {
		t.Fatalf("Load() = %v, want OUTBOX_LEASE reported", err)
	}
}

func TestLoadRefusesMoreMinimumThanMaximumConnections(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/orders")
	t.Setenv("DB_MIN_CONNS", "30")
	t.Setenv("DB_MAX_CONNS", "5")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS") {
		t.Fatalf("Load() = %v, want DB_MIN_CONNS reported", err)
	}
}
