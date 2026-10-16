package pgxx_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/avito-kitchen/platform/test/testenv"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newDB returns a pool on a fresh database with a small table of its own.
func newDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testenv.RequireDB(t, migrations.DirOrders)
	if _, err := pool.Exec(testCtx(t), `
CREATE TABLE pgxx_items (
    id     integer PRIMARY KEY,
    qty    integer NOT NULL CONSTRAINT pgxx_items_qty_chk CHECK (qty >= 0),
    parent integer REFERENCES pgxx_items (id)
)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return pool
}

// count reads through the pool, never through the ambient transaction, so it
// sees committed rows only.
func count(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pgxx_items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func insert(ctx context.Context, tx *pgxx.TxManager, id, qty int) error {
	_, err := tx.Querier(ctx).Exec(ctx, `INSERT INTO pgxx_items (id, qty) VALUES ($1, $2)`, id, qty)
	return err
}

func TestInTxCommitsOnSuccess(t *testing.T) {
	pool := newDB(t)
	tx := pgxx.NewTxManager(pool)
	ctx := testCtx(t)

	err := tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		if err := insert(ctx, tx, 1, 1); err != nil {
			return err
		}
		// Inside the transaction the row is visible to the transaction only.
		if got := count(ctx, t, pool); got != 0 {
			return fmt.Errorf("uncommitted row visible outside: %d", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx() = %v", err)
	}
	if got := count(testCtx(t), t, pool); got != 1 {
		t.Fatalf("rows after commit = %d, want 1", got)
	}
}

func TestInTxRollsBackOnErrorAndReturnsIt(t *testing.T) {
	pool := newDB(t)
	tx := pgxx.NewTxManager(pool)
	sentinel := errors.New("business refusal")

	err := tx.InTx(testCtx(t), pgx.TxOptions{}, func(ctx context.Context) error {
		if err := insert(ctx, tx, 1, 1); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx() = %v, want the error of fn", err)
	}
	if got := count(testCtx(t), t, pool); got != 0 {
		t.Fatalf("rows after rollback = %d, want 0", got)
	}
}

// A nested call joins the outer transaction, so the outer outcome decides.
func TestNestedInTxJoinsTheOuterTransaction(t *testing.T) {
	pool := newDB(t)
	tx := pgxx.NewTxManager(pool)
	sentinel := errors.New("outer refusal")

	err := tx.InTx(testCtx(t), pgx.TxOptions{}, func(ctx context.Context) error {
		if err := insert(ctx, tx, 1, 1); err != nil {
			return err
		}
		if err := tx.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
			return insert(ctx, tx, 2, 1)
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx() = %v", err)
	}
	if got := count(testCtx(t), t, pool); got != 0 {
		t.Fatalf("rows = %d, want 0: the nested write must roll back with the outer one", got)
	}
}

func TestInTxRollsBackOnPanic(t *testing.T) {
	pool := newDB(t)
	tx := pgxx.NewTxManager(pool)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic must propagate")
			}
		}()
		_ = tx.InTx(testCtx(t), pgx.TxOptions{}, func(ctx context.Context) error {
			if err := insert(ctx, tx, 1, 1); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	if got := count(testCtx(t), t, pool); got != 0 {
		t.Fatalf("rows after a panic = %d, want 0", got)
	}
}

// The classification is what turns a constraint into a domain error.
func TestErrorClassification(t *testing.T) {
	pool := newDB(t)
	tx := pgxx.NewTxManager(pool)
	ctx := testCtx(t)
	if err := insert(ctx, tx, 1, 1); err != nil {
		t.Fatalf("insert: %v", err)
	}

	dup := insert(ctx, tx, 1, 1)
	if !pgxx.IsUniqueViolation(dup) || pgxx.IsCheckViolation(dup) {
		t.Fatalf("duplicate key = %v, want a unique violation only", dup)
	}
	if got := pgxx.ConstraintName(dup); got != "pgxx_items_pkey" {
		t.Fatalf("ConstraintName = %q, want pgxx_items_pkey", got)
	}

	check := insert(ctx, tx, 2, -1)
	if !pgxx.IsCheckViolation(fmt.Errorf("wrapped: %w", check)) {
		t.Fatalf("negative qty = %v, want a check violation through wrapping", check)
	}
	if got := pgxx.ConstraintName(check); got != "pgxx_items_qty_chk" {
		t.Fatalf("ConstraintName = %q, want pgxx_items_qty_chk", got)
	}

	_, fk := pool.Exec(ctx, `INSERT INTO pgxx_items (id, qty, parent) VALUES (3, 1, 999)`)
	if !pgxx.IsForeignKeyViolation(fk) {
		t.Fatalf("unknown parent = %v, want a foreign key violation", fk)
	}

	for _, code := range []string{"40001", "40P01"} {
		if !pgxx.IsRetryable(&pgconn.PgError{Code: code}) {
			t.Fatalf("SQLSTATE %s must be retryable", code)
		}
	}
	plain := errors.New("not a database error")
	if pgxx.IsUniqueViolation(plain) || pgxx.IsRetryable(plain) || pgxx.ConstraintName(plain) != "" {
		t.Fatal("an error without SQLSTATE must not be classified")
	}
}

// The statement timeout of the pool is applied to every connection, and its
// cancellation is recognised.
func TestPoolAppliesTheStatementTimeout(t *testing.T) {
	pool := newDB(t)
	ctx := testCtx(t)

	limited, err := pgxx.NewPool(ctx, pgxx.PoolConfig{
		DSN: pool.Config().ConnString(), MaxConns: 2, MinConns: 1,
		ConnectTimeout: 5 * time.Second, StatementTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewPool() = %v", err)
	}
	t.Cleanup(limited.Close)

	var setting string
	if err := limited.QueryRow(ctx, `SHOW statement_timeout`).Scan(&setting); err != nil {
		t.Fatalf("show statement_timeout: %v", err)
	}
	if setting != "100ms" {
		t.Fatalf("statement_timeout = %q, want 100ms", setting)
	}
	_, err = limited.Exec(ctx, `SELECT pg_sleep(2)`)
	if !pgxx.IsStatementTimeout(err) {
		t.Fatalf("slow statement = %v, want a statement timeout", err)
	}
}

func TestNewPoolReportsBadConfiguration(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	if _, err := pgxx.NewPool(ctx, pgxx.PoolConfig{DSN: "postgres://%zz", MaxConns: 1}); err == nil {
		t.Fatal("a malformed DSN must be refused")
	}
	// Nothing listens on port 1: the ping fails within the connect timeout.
	start := time.Now()
	_, err := pgxx.NewPool(ctx, pgxx.PoolConfig{
		DSN:      "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
		MaxConns: 1, ConnectTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("an unreachable server must be reported")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the failure took %s, want it bounded by the connect timeout", time.Since(start))
	}
}
