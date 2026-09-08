// Package pgmigrate applies versioned SQL migrations from an embedded file
// system. Files are named <version>_<name>.sql and applied in ascending
// version order, each inside its own transaction, guarded by a PostgreSQL
// advisory lock so that concurrent instances cannot apply them twice.
package pgmigrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockID is a fixed application-defined key for the migration lock.
const advisoryLockID int64 = 8264391027465001

// Migration is one versioned SQL file.
type Migration struct {
	Version int64
	Name    string
	SQL     string
}

// Load reads and validates migrations from dir inside fsys.
func Load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var out []Migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		idx := strings.Index(base, "_")
		if idx <= 0 {
			return nil, fmt.Errorf("migration %q must be named <version>_<name>.sql", e.Name())
		}
		version, err := strconv.ParseInt(base[:idx], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", e.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %q and %q", version, prev, e.Name())
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			return nil, fmt.Errorf("migration %q is empty", e.Name())
		}
		out = append(out, Migration{Version: version, Name: base[idx+1:], SQL: string(body)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations found in %q", dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Apply brings the schema up to date. It is safe to run concurrently: the
// advisory lock serialises instances, and already applied versions are
// skipped. Every migration runs in its own transaction, so a failure leaves
// the previous versions applied and the failing one not recorded.
func Apply(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, migrations []Migration) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, advisoryLockID); err != nil {
			logger.Error("release advisory lock", slog.Any("error", err))
		}
	}()

	const createTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     bigint      PRIMARY KEY,
    name        text        NOT NULL,
    applied_at  timestamptz NOT NULL DEFAULT now()
)`
	if _, err := conn.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[int64]struct{}{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[v] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema_migrations: %w", err)
	}

	for _, m := range migrations {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if err := applyOne(ctx, conn.Conn(), m); err != nil {
			return fmt.Errorf("apply migration %d_%s: %w", m.Version, m.Name, err)
		}
		logger.Info("migration applied",
			slog.Int64("version", m.Version), slog.String("name", m.Name))
	}
	return nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
		m.Version, m.Name); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Status returns the highest applied version, or 0 when none were applied.
func Status(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var version int64
	err := pool.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read migration status: %w", err)
	}
	return version, nil
}
