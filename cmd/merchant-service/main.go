// Command merchant-service runs the merchant microservice of Avito.Kitchen.
//
// It owns the closed list of connected outlets, their access keys and their
// commercial profile. It serves the external HTTP contract for customers and
// merchants, and the internal merchant.v1.MerchantService gRPC contract for
// catalog-service, inventory-service and order-service.
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

	"github.com/avito-kitchen/platform/internal/merchant/adapters/grpcapi"
	"github.com/avito-kitchen/platform/internal/merchant/adapters/httpapi"
	pgstore "github.com/avito-kitchen/platform/internal/merchant/adapters/postgres"
	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/config"
	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/grpcx"
	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/avito-kitchen/platform/pkg/pgmigrate"
	"github.com/avito-kitchen/platform/pkg/pgxx"
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
		slog.String("grpc_addr", cfg.GRPCAddr),
		slog.String("ops_addr", cfg.OpsAddr),
		slog.String("database", logx.RedactDSN(cfg.DatabaseURL)),
	)

	// Signal handling drives a single cancellation for every component.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxx.NewPool(ctx, pgxx.PoolConfig{
		DSN:              cfg.DatabaseURL,
		MaxConns:         cfg.DBMaxConns,
		MinConns:         cfg.DBMinConns,
		MaxConnLifetime:  cfg.DBMaxConnLife,
		MaxConnIdleTime:  cfg.DBMaxConnIdle,
		ConnectTimeout:   cfg.DBConnectTimeout,
		StatementTimeout: cfg.DBStatementMax,
	})
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	// Applying migrations from the service is opt-in. The default deployment
	// runs the dedicated migrate job before the services start, so that N
	// instances do not race and a schema failure is visible on its own.
	if cfg.RunMigrations {
		files, loadErr := pgmigrate.Load(migrations.FS, migrations.DirMerchant)
		if loadErr != nil {
			return loadErr
		}
		if applyErr := pgmigrate.Apply(ctx, pool, logger, files); applyErr != nil {
			return fmt.Errorf("apply migrations: %w", applyErr)
		}
	}

	txManager := pgxx.NewTxManager(pool)
	service := app.NewService(
		pgstore.NewMerchantRepository(txManager),
		pgstore.NewAPIKeyRepository(txManager),
		cfg.MaxMerchantBatch,
	)

	// Public HTTP API.
	apiHandler := httpapi.NewHandler(service, logger).Routes()
	publicHandler := httpx.Chain(apiHandler,
		httpx.RequestID(),
		httpx.Recover(logger),
		httpx.AccessLog(logger),
		httpx.BodyLimit(cfg.HTTPMaxBodyBytes),
		httpx.Timeout(cfg.HTTPHandlerTimeout),
	)
	publicSrv := httpx.NewServer(httpx.ServerConfig{
		Addr:              cfg.HTTPAddr,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}, publicHandler)

	// Internal ops listener: liveness and readiness only, never exposed publicly.
	ops := httpx.NewOpsHandler(func(ctx context.Context) error {
		return pool.Ping(ctx)
	}, cfg.ReadinessTimeout)
	opsSrv := httpx.NewServer(httpx.ServerConfig{
		Addr:              cfg.OpsAddr,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}, ops)

	// Internal gRPC API.
	grpcSrv := grpcx.NewServer(grpcx.ServerConfig{
		Addr:             cfg.GRPCAddr,
		MaxConnectionAge: cfg.GRPCMaxConnectionAge,
		KeepaliveTime:    cfg.GRPCKeepaliveTime,
		KeepaliveTimeout: cfg.GRPCKeepaliveTimeout,
		MaxRecvMsgBytes:  cfg.GRPCMaxRecvMsgBytes,
		HandlerTimeout:   cfg.GRPCHandlerTimeout,
	}, logger)
	grpcapi.NewServer(service).Register(grpcSrv.Registrar())
	grpcSrv.SetServing("", true)
	grpcSrv.SetServing(grpcapi.ServiceName, true)

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

	// A failure in any listener cancels the others.
	runCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	wg.Add(3)
	go func() {
		defer wg.Done()
		defer cancelAll()
		record("public http", httpx.Serve(runCtx, publicSrv, logger, cfg.ShutdownTimeout))
	}()
	go func() {
		defer wg.Done()
		defer cancelAll()
		record("ops http", httpx.Serve(runCtx, opsSrv, logger, cfg.ShutdownTimeout))
	}()
	go func() {
		defer wg.Done()
		defer cancelAll()
		record("grpc", grpcSrv.Serve(runCtx, cfg.ShutdownTimeout))
	}()

	<-runCtx.Done()
	// Report not-ready before the listeners close so a balancer drains us first.
	ops.BeginShutdown()
	grpcSrv.SetServing("", false)
	grpcSrv.SetServing(grpcapi.ServiceName, false)
	logger.Info("shutdown started", slog.Duration("grace", cfg.ShutdownTimeout))

	wg.Wait()
	logger.Info("shutdown complete")
	return errors.Join(errs...)
}
