package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/config"
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
	"SERVICE_NAME", "HTTP_ADDR", "OPS_ADDR", "CATALOG_BASE_URL", "INVENTORY_BASE_URL",
	"ORDER_BASE_URL", "PARTNER_POS_API_KEY", "PLATFORM_TIMEOUT", "PUBLISH_ON_START",
	"STOCK_SYNC_INTERVAL", "ORDER_POLL_INTERVAL", "KITCHEN_STEP", "ORDER_PAGE_LIMIT",
	"HTTP_HANDLER_TIMEOUT", "SHUTDOWN_TIMEOUT",
}

func TestLoadAppliesTheDefaults(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("PARTNER_POS_API_KEY", "dev-merchant-key-kafe-primer-0001")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.ServiceName != "partner-pos" || cfg.HTTPAddr != ":8080" || cfg.OpsAddr != ":9091" ||
		cfg.CatalogBaseURL != "http://catalog-service:8080" ||
		cfg.InventoryBaseURL != "http://inventory-service:8080" ||
		cfg.OrderBaseURL != "http://order-service:8080" {
		t.Fatalf("names and addresses = %+v", cfg)
	}
	// The queue is polled every five seconds (scenario S-M6).
	if cfg.PlatformTimeout != 5*time.Second || !cfg.PublishOnStart || cfg.StockSyncInterval != time.Minute ||
		cfg.PollInterval != 5*time.Second || cfg.KitchenStep != 20*time.Second || cfg.OrderPageLimit != 50 ||
		cfg.HTTPHandlerTimeout != 30*time.Second || cfg.ShutdownTimeout != 15*time.Second {
		t.Fatalf("defaults = %+v", cfg)
	}
}

// Without the key of the outlet the till could only collect 401s.
func TestLoadRequiresTheAccessKey(t *testing.T) {
	isolate(t, keys...)
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "PARTNER_POS_API_KEY") {
		t.Fatalf("Load() = %v, want PARTNER_POS_API_KEY reported", err)
	}
}

// POST /pos/publish makes two platform calls inside one request, so the
// handler timeout must exceed the timeout of a single call.
func TestLoadRefusesAHandlerTimeoutWithinThePlatformTimeout(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("PARTNER_POS_API_KEY", "key")
	t.Setenv("HTTP_HANDLER_TIMEOUT", "5s")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "HTTP_HANDLER_TIMEOUT") {
		t.Fatalf("Load() = %v, want HTTP_HANDLER_TIMEOUT reported", err)
	}
}

func TestLoadReportsMalformedValues(t *testing.T) {
	isolate(t, keys...)
	t.Setenv("PARTNER_POS_API_KEY", "key")
	t.Setenv("PUBLISH_ON_START", "sometimes")
	t.Setenv("ORDER_PAGE_LIMIT", "0")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() = nil, want the malformed values reported")
	}
	for _, key := range []string{"PUBLISH_ON_START", "ORDER_PAGE_LIMIT"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("Load() = %q, want %s reported", err, key)
		}
	}
}
