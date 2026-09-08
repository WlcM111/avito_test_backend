// Package config loads and validates the partner-pos configuration from the
// environment. All values are validated at start-up: the process refuses to
// open its listener with an invalid configuration.
package config

import (
	"fmt"
	"time"

	"github.com/avito-kitchen/platform/pkg/envcfg"
)

// Config is the complete configuration of partner-pos.
//
// The till has no database: everything it needs is the address of the three
// public APIs and the access key of the outlet.
type Config struct {
	ServiceName string
	LogLevel    string

	HTTPAddr string
	OpsAddr  string

	CatalogBaseURL   string
	InventoryBaseURL string
	OrderBaseURL     string
	APIKey           string

	PlatformTimeout time.Duration

	PublishOnStart    bool
	StockSyncInterval time.Duration
	PollInterval      time.Duration
	KitchenStep       time.Duration
	OrderPageLimit    int

	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	HTTPHandlerTimeout    time.Duration
	HTTPMaxBodyBytes      int64

	ShutdownTimeout time.Duration
}

// Load reads the configuration and reports every problem at once.
func Load() (Config, error) {
	l := envcfg.New()
	cfg := Config{
		ServiceName: l.String("SERVICE_NAME", "partner-pos"),
		LogLevel:    l.String("LOG_LEVEL", "info"),

		HTTPAddr: l.String("HTTP_ADDR", ":8080"),
		OpsAddr:  l.String("OPS_ADDR", ":9091"),

		CatalogBaseURL:   l.String("CATALOG_BASE_URL", "http://catalog-service:8080"),
		InventoryBaseURL: l.String("INVENTORY_BASE_URL", "http://inventory-service:8080"),
		OrderBaseURL:     l.String("ORDER_BASE_URL", "http://order-service:8080"),
		// The access key of the outlet. It is required: without it the till
		// cannot act as a merchant, and starting with an empty key would only
		// produce a stream of 401s.
		APIKey: l.Required("PARTNER_POS_API_KEY"),

		PlatformTimeout: l.Duration("PLATFORM_TIMEOUT", 5*time.Second),

		PublishOnStart:    l.Bool("PUBLISH_ON_START", true),
		StockSyncInterval: l.Duration("STOCK_SYNC_INTERVAL", time.Minute),
		// The specification fixes the polling of the merchant queue at five
		// seconds (scenario S-M6).
		PollInterval:   l.Duration("ORDER_POLL_INTERVAL", 5*time.Second),
		KitchenStep:    l.Duration("KITCHEN_STEP", 20*time.Second),
		OrderPageLimit: l.PositiveInt("ORDER_PAGE_LIMIT", 50),

		HTTPReadHeaderTimeout: l.Duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		HTTPReadTimeout:       l.Duration("HTTP_READ_TIMEOUT", 15*time.Second),
		HTTPWriteTimeout:      l.Duration("HTTP_WRITE_TIMEOUT", 20*time.Second),
		HTTPIdleTimeout:       l.Duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		HTTPHandlerTimeout:    l.Duration("HTTP_HANDLER_TIMEOUT", 30*time.Second),
		HTTPMaxBodyBytes:      int64(l.PositiveInt("HTTP_MAX_BODY_BYTES", 64*1024)),

		ShutdownTimeout: l.Duration("SHUTDOWN_TIMEOUT", 15*time.Second),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	// The control API publishes the menu and synchronises the stock in one
	// request, so its handler timeout has to cover both platform calls.
	if cfg.HTTPHandlerTimeout <= cfg.PlatformTimeout {
		return Config{}, fmt.Errorf(
			"invalid configuration: HTTP_HANDLER_TIMEOUT (%s) must exceed PLATFORM_TIMEOUT (%s)",
			cfg.HTTPHandlerTimeout, cfg.PlatformTimeout)
	}
	return cfg, nil
}
