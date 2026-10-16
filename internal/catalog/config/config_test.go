package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/catalog/config"
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
	"DB_MAX_CONNS", "DB_MIN_CONNS", "DB_STATEMENT_TIMEOUT", "HTTP_MAX_BODY_BYTES",
	"MERCHANT_GRPC_TARGET", "MAX_QUOTE_BATCH",
}

func TestLoadAppliesTheDefaults(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://catalog_svc:pw@postgres:5432/catalog")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.ServiceName != "catalog-service" || cfg.HTTPAddr != ":8080" ||
		cfg.GRPCAddr != ":9090" || cfg.OpsAddr != ":9091" ||
		cfg.MerchantGRPCTarget != "dns:///merchant-service:9090" {
		t.Fatalf("names and addresses = %+v", cfg)
	}
	// A published snapshot carries a whole menu: the body limit is 1 MiB.
	if cfg.HTTPMaxBodyBytes != 1024*1024 || cfg.DBStatementMax != 5*time.Second || cfg.MaxQuoteBatch != 100 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadRequiresTheDatabase(t *testing.T) {
	isolate(t, keys...)
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() = %v, want DATABASE_URL reported", err)
	}
}

func TestLoadRefusesMoreMinimumThanMaximumConnections(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/catalog")
	t.Setenv("DB_MIN_CONNS", "11")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS") {
		t.Fatalf("Load() = %v, want DB_MIN_CONNS reported", err)
	}
}

func TestLoadReportsANonPositiveLimit(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("DATABASE_URL", "postgres://localhost/catalog")
	t.Setenv("MAX_QUOTE_BATCH", "-1")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "MAX_QUOTE_BATCH") {
		t.Fatalf("Load() = %v, want MAX_QUOTE_BATCH reported", err)
	}
}
