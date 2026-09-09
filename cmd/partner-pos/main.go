// Command partner-pos runs the demonstration till of one outlet.
//
// It models an EXTERNAL organisation (TZ v3.0 §3.3, requirement A-06): it owns
// no platform data, has no database and no access to the internal gRPC
// network. Everything it does goes through the public HTTP APIs with the
// access key of the outlet - publishing the menu to catalog-service, keeping
// the quantities in inventory-service in step with its own kitchen, and
// answering the order queue of order-service.
//
// Its own HTTP API exists to steer the demonstration: read and change the till
// menu, publish it, look at the queue and switch how the till answers orders.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/adapters/httpapi"
	"github.com/avito-kitchen/platform/internal/partnerpos/adapters/platform"
	"github.com/avito-kitchen/platform/internal/partnerpos/app"
	"github.com/avito-kitchen/platform/internal/partnerpos/config"
	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/avito-kitchen/platform/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		slog.Default().Error("service stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logx.New(cfg.ServiceName, cfg.LogLevel)
	logger.Info("starting",
		slog.String("http_addr", cfg.HTTPAddr),
		slog.String("ops_addr", cfg.OpsAddr),
		slog.String("catalog", cfg.CatalogBaseURL),
		slog.String("inventory", cfg.InventoryBaseURL),
		slog.String("order", cfg.OrderBaseURL),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := platform.NewClient(platform.Config{
		CatalogBaseURL:   cfg.CatalogBaseURL,
		InventoryBaseURL: cfg.InventoryBaseURL,
		OrderBaseURL:     cfg.OrderBaseURL,
		APIKey:           cfg.APIKey,
		Timeout:          cfg.PlatformTimeout,
	})
	// Публикация меню и выталкивание остатков живут в фоновом цикле, а не
	// здесь: первый его проход выполняется сразу и делает то же самое, но
	// повторяется до успеха, если платформа ещё прогревается. Отдельная
	// «жадная» попытка при старте давала ровно одну попытку — после её сбоя
	// касса оставалась неопубликованной до ручного POST /pos/publish.
	service := app.NewService(client, domain.DefaultMenu(), logger, app.Config{
		StockSyncInterval: cfg.StockSyncInterval,
		PollInterval:      cfg.PollInterval,
		KitchenStep:       cfg.KitchenStep,
		OrderPageLimit:    cfg.OrderPageLimit,
		AutoPublish:       cfg.PublishOnStart,
	})

	controlHandler := httpx.Chain(httpapi.NewHandler(service, logger).Routes(),
		httpx.RequestID(),
		httpx.Recover(logger),
		httpx.AccessLog(logger),
		httpx.BodyLimit(cfg.HTTPMaxBodyBytes),
		httpx.Timeout(cfg.HTTPHandlerTimeout),
	)
	controlSrv := httpx.NewServer(httpx.ServerConfig{
		Addr:              cfg.HTTPAddr,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}, controlHandler)

	// Readiness reports only what this process owns. The till stays ready
	// while the platform is down: it retries on its own, and taking it out of
	// rotation would hide the very outage the demonstration is meant to show.
	ops := httpx.NewOpsHandler(func(context.Context) error { return nil }, time.Second)
	opsSrv := httpx.NewServer(httpx.ServerConfig{
		Addr:              cfg.OpsAddr,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}, ops)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	record := func(name string, err error) {
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		mu.Lock()
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
		mu.Unlock()
	}

	runCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	wg.Add(4)
	go func() {
		defer wg.Done()
		defer cancelAll()
		record("control http", httpx.Serve(runCtx, controlSrv, logger, cfg.ShutdownTimeout))
	}()
	go func() {
		defer wg.Done()
		defer cancelAll()
		record("ops http", httpx.Serve(runCtx, opsSrv, logger, cfg.ShutdownTimeout))
	}()
	go func() {
		defer wg.Done()
		defer cancelAll()
		record("stock sync", service.RunStockSync(runCtx))
	}()
	go func() {
		defer wg.Done()
		defer cancelAll()
		record("order polling", service.RunOrderPolling(runCtx))
	}()

	<-runCtx.Done()
	ops.BeginShutdown()
	logger.Info("shutdown started", slog.Duration("grace", cfg.ShutdownTimeout))

	wg.Wait()
	logger.Info("shutdown complete")
	return errors.Join(errs...)
}
