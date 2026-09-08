// Command order-service runs the order microservice of Avito.Kitchen.
//
// It owns the order and its lifecycle: preview, idempotent placement, the
// merchant queue, the status transitions and the reliable delivery of a
// terminal decision to inventory-service through a transactional outbox.
//
// It is the only service that depends on all three others and the only one
// with a background worker. It exposes no gRPC server, because nothing in the
// system calls it.
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

	"github.com/avito-kitchen/platform/internal/order/adapters/grpcclient"
	"github.com/avito-kitchen/platform/internal/order/adapters/httpapi"
	pgstore "github.com/avito-kitchen/platform/internal/order/adapters/postgres"
	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/config"
	"github.com/avito-kitchen/platform/internal/order/jobs"
	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/grpcx"
	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/avito-kitchen/platform/pkg/pgmigrate"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"google.golang.org/grpc"
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
		slog.String("database", logx.RedactDSN(cfg.DatabaseURL)),
		slog.String("merchant_target", cfg.MerchantGRPCTarget),
		slog.String("catalog_target", cfg.CatalogGRPCTarget),
		slog.String("inventory_target", cfg.InventoryGRPCTarget),
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
	// runs the dedicated migrate job before the services start.
	if cfg.RunMigrations {
		files, loadErr := pgmigrate.Load(migrations.FS, migrations.DirOrders)
		if loadErr != nil {
			return loadErr
		}
		if applyErr := pgmigrate.Apply(ctx, pool, logger, files); applyErr != nil {
			return fmt.Errorf("apply migrations: %w", applyErr)
		}
	}

	// Outbound connections. Dialling is lazy, so a dependency that is not up
	// yet does not stop the start-up; the calls that need it answer 503 until
	// it is.
	conns, closeConns, err := dialDependencies(cfg, logger)
	if err != nil {
		return err
	}
	defer closeConns()

	txManager := pgxx.NewTxManager(pool)
	service := app.NewService(
		pgstore.NewOrderRepository(txManager),
		pgstore.NewOutboxRepository(txManager),
		grpcclient.NewMerchantDirectory(conns.merchant,
			cfg.MerchantAuthTimeout, cfg.MerchantProfileTimeout),
		grpcclient.NewMenuCatalogue(conns.catalog, cfg.CatalogQuoteTimeout),
		grpcclient.NewStockLedger(conns.inventory, cfg.InventoryReadTimeout,
			cfg.InventoryReserveTimeout, cfg.InventoryDecisionTimeout),
		logger,
		app.Config{
			ReservationTTL:      cfg.ReservationTTL,
			MaxDeliveryAttempts: cfg.MaxDeliveryAttempts,
		},
	)

	// Public HTTP API.
	publicHandler := httpx.Chain(httpapi.NewHandler(service, logger).Routes(),
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

	// Internal ops listener. Readiness checks only what this service owns: an
	// outage of a dependency must not take order-service out of rotation, it
	// surfaces as 503 on the endpoints that need that dependency.
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

	dispatcher := jobs.NewOutboxDispatcher(service, logger,
		cfg.OutboxInterval, cfg.OutboxBatch, cfg.OutboxLease, cfg.OutboxTimeout)

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

	// A failure in any component cancels the others.
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
		record("outbox dispatcher", dispatcher.Run(runCtx))
	}()

	<-runCtx.Done()
	// Report not-ready before the listeners close so a balancer drains us
	// first. Decisions already written to the outbox are not lost by stopping:
	// they stay PENDING and the next instance picks them up.
	ops.BeginShutdown()
	logger.Info("shutdown started", slog.Duration("grace", cfg.ShutdownTimeout))

	wg.Wait()
	if pending, err := service.PendingOutboxCount(context.WithoutCancel(ctx)); err == nil && pending > 0 {
		logger.Warn("outbox still has undelivered decisions",
			slog.Int("pending", pending))
	}
	logger.Info("shutdown complete")
	return errors.Join(errs...)
}

// dependencies holds the outbound connections of the service.
type dependencies struct {
	merchant  *grpc.ClientConn
	catalog   *grpc.ClientConn
	inventory *grpc.ClientConn
}

// dialDependencies creates the three client connections and returns a closer
// that releases every connection that was actually created, including on a
// partially failed start-up.
func dialDependencies(cfg config.Config, logger *slog.Logger) (dependencies, func(), error) {
	var (
		out    dependencies
		opened []*grpc.ClientConn
	)
	closeAll := func() {
		for _, conn := range opened {
			if err := conn.Close(); err != nil {
				logger.Error("close grpc connection", slog.Any("error", err))
			}
		}
	}
	dial := func(target string) (*grpc.ClientConn, error) {
		conn, err := grpcx.Dial(grpcx.ClientConfig{
			Target:           target,
			KeepaliveTime:    cfg.GRPCKeepaliveTime,
			KeepaliveTimeout: cfg.GRPCKeepaliveTimeout,
			MaxRecvMsgBytes:  cfg.GRPCMaxRecvMsgBytes,
		}, logger)
		if err != nil {
			return nil, err
		}
		opened = append(opened, conn)
		return conn, nil
	}

	var err error
	if out.merchant, err = dial(cfg.MerchantGRPCTarget); err != nil {
		closeAll()
		return dependencies{}, nil, fmt.Errorf("connect to merchant-service: %w", err)
	}
	if out.catalog, err = dial(cfg.CatalogGRPCTarget); err != nil {
		closeAll()
		return dependencies{}, nil, fmt.Errorf("connect to catalog-service: %w", err)
	}
	if out.inventory, err = dial(cfg.InventoryGRPCTarget); err != nil {
		closeAll()
		return dependencies{}, nil, fmt.Errorf("connect to inventory-service: %w", err)
	}
	return out, closeAll, nil
}
