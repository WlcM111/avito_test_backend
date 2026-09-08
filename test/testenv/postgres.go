// Package testenv provides a real PostgreSQL instance for integration tests.
//
// Tests that need storage call RequireDB. It reads TEST_DATABASE_URL, creates
// an isolated database for the test, applies the committed migrations and
// drops the database afterwards. When TEST_DATABASE_URL is unset the test is
// skipped, so `go test ./...` stays runnable without a database while the
// integration suite always runs against real SQL rather than a mock.
package testenv

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgmigrate"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvDatabaseURL is the environment variable holding the admin connection
// string used to create per-test databases.
const EnvDatabaseURL = "TEST_DATABASE_URL"

var counter atomic.Int64

// RequireDB returns a pool connected to a fresh database with the migrations
// of migrationDir applied. The database is dropped when the test finishes.
func RequireDB(t *testing.T, migrationDir string) *pgxpool.Pool {
	t.Helper()
	adminDSN := strings.TrimSpace(os.Getenv(EnvDatabaseURL))
	if adminDSN == "" {
		t.Skipf("%s is not set; skipping the integration test", EnvDatabaseURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	admin, err := pgxx.NewPool(ctx, pgxx.PoolConfig{
		DSN: adminDSN, MaxConns: 2, MinConns: 1, ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvDatabaseURL, err)
	}
	defer admin.Close()

	dbName := fmt.Sprintf("mkt_%d_%s", counter.Add(1),
		strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	if _, err := admin.Exec(ctx, `CREATE DATABASE "`+dbName+`"`); err != nil {
		t.Fatalf("create database %s: %v", dbName, err)
	}

	testDSN, err := replaceDatabase(adminDSN, dbName)
	if err != nil {
		t.Fatalf("build test dsn: %v", err)
	}
	pool, err := pgxx.NewPool(ctx, pgxx.PoolConfig{
		DSN: testDSN, MaxConns: 8, MinConns: 1, ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}

	files, err := pgmigrate.Load(migrations.FS, migrationDir)
	if err != nil {
		pool.Close()
		t.Fatalf("load migrations: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := pgmigrate.Apply(ctx, pool, quiet, files); err != nil {
		pool.Close()
		t.Fatalf("apply migrations: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		dropPool, err := pgxx.NewPool(dropCtx, pgxx.PoolConfig{
			DSN: adminDSN, MaxConns: 1, MinConns: 1, ConnectTimeout: 10 * time.Second,
		})
		if err != nil {
			t.Logf("cleanup: connect: %v", err)
			return
		}
		defer dropPool.Close()
		if _, err := dropPool.Exec(dropCtx,
			`DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`); err != nil {
			t.Logf("cleanup: drop database %s: %v", dbName, err)
		}
	})
	return pool
}

func replaceDatabase(dsn, dbName string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

// MigrationVersion returns the highest applied migration version of the
// database behind pool.
func MigrationVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	return pgmigrate.Status(ctx, pool)
}

// ApplyAgain re-applies the migrations of migrationDir. It exists so that a
// test can prove the migration runner is idempotent.
func ApplyAgain(ctx context.Context, pool *pgxpool.Pool, migrationDir string) error {
	files, err := pgmigrate.Load(migrations.FS, migrationDir)
	if err != nil {
		return err
	}
	return pgmigrate.Apply(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil)), files)
}
