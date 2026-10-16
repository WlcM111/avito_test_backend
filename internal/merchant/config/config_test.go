package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/merchant/config"
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
	"DB_MAX_CONNS", "DB_MIN_CONNS", "HTTP_READ_TIMEOUT", "HTTP_MAX_BODY_BYTES",
	"SHUTDOWN_TIMEOUT", "MAX_MERCHANT_BATCH", "RUN_MIGRATIONS",
}

func TestLoadAppliesTheDefaults(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://merchant_svc:pw@postgres:5432/merchant")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.ServiceName != "merchant-service" || cfg.HTTPAddr != ":8080" ||
		cfg.GRPCAddr != ":9090" || cfg.OpsAddr != ":9091" {
		t.Fatalf("names and addresses = %+v", cfg)
	}
	if cfg.DBMaxConns != 10 || cfg.DBMinConns != 2 || cfg.HTTPMaxBodyBytes != 64*1024 ||
		cfg.ShutdownTimeout != 25*time.Second || cfg.MaxMerchantBatch != 100 || cfg.RunMigrations {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadRequiresTheDatabase(t *testing.T) {
	isolate(t, keys...)
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() = %v, want DATABASE_URL reported", err)
	}
}

// Every malformed value is reported at once, so one start shows everything.
func TestLoadReportsEveryMalformedValue(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/merchant")
	t.Setenv("DB_MAX_CONNS", "0")
	t.Setenv("HTTP_READ_TIMEOUT", "soon")
	t.Setenv("RUN_MIGRATIONS", "maybe")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() = nil, want the malformed values reported")
	}
	for _, key := range []string{"DB_MAX_CONNS", "HTTP_READ_TIMEOUT", "RUN_MIGRATIONS"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("Load() = %q, want %s reported", err, key)
		}
	}
}

func TestLoadRefusesMoreMinimumThanMaximumConnections(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/merchant")
	t.Setenv("DB_MIN_CONNS", "20")
	t.Setenv("DB_MAX_CONNS", "10")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS") {
		t.Fatalf("Load() = %v, want DB_MIN_CONNS reported", err)
	}
}
