// Command migrate applies the embedded SQL migrations of one service database.
//
// It is deployed as a short-lived job that runs before the services start:
//
//	MIGRATE_SERVICE=merchant DATABASE_URL=postgres://... migrate
//
// Running it concurrently is safe: an advisory lock serialises the instances.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/envcfg"
	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/avito-kitchen/platform/pkg/pgmigrate"
	"github.com/avito-kitchen/platform/pkg/pgxx"
)

func main() {
	if err := run(); err != nil {
		slog.Default().Error("migration failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	l := envcfg.New()
	service := l.String("MIGRATE_SERVICE", migrations.DirMerchant)
	dsn := l.Required("DATABASE_URL")
	timeout := l.Duration("MIGRATE_TIMEOUT", 2*time.Minute)
	logLevel := l.String("LOG_LEVEL", "info")
	if err := l.Err(); err != nil {
		return err
	}
	if !slices.Contains(migrations.Known, service) {
		return fmt.Errorf("unknown MIGRATE_SERVICE %q, expected one of %v", service, migrations.Known)
	}

	logger := logx.New("migrate", logLevel).With(slog.String("target", service))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	files, err := pgmigrate.Load(migrations.FS, service)
	if err != nil {
		return err
	}

	pool, err := pgxx.NewPool(ctx, pgxx.PoolConfig{
		DSN:            dsn,
		MaxConns:       2,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("connect to %s: %w", logx.RedactDSN(dsn), err)
	}
	defer pool.Close()

	if err := pgmigrate.Apply(ctx, pool, logger, files); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("migration timed out after %s: %w", timeout, err)
		}
		return err
	}
	version, err := pgmigrate.Status(ctx, pool)
	if err != nil {
		return err
	}
	logger.Info("migrations up to date", slog.Int64("version", version))
	return nil
}
