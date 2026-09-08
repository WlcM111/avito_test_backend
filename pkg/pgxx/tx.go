package pgxx

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the subset of pgx used by repositories. Both *pgxpool.Pool and
// pgx.Tx satisfy it, so a repository method works inside or outside a
// transaction without knowing which.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// TxManager owns transaction boundaries. Use cases call InTx; repositories
// call Querier to pick up the ambient transaction.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager returns a TxManager bound to the pool.
func NewTxManager(pool *pgxpool.Pool) *TxManager { return &TxManager{pool: pool} }

// InTx runs fn inside a transaction, committing on success and rolling back on
// error or panic. Nested calls reuse the outer transaction.
func (m *TxManager) InTx(ctx context.Context, opts pgx.TxOptions, fn func(context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}
	tx, err := m.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txCtx := context.WithValue(ctx, txKey{}, tx)

	panicked := true
	defer func() {
		if panicked {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err := fn(txCtx); err != nil {
		panicked = false
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !isTxClosed(rbErr) {
			return fmt.Errorf("%w (rollback failed: %v)", err, rbErr)
		}
		return err
	}
	panicked = false
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// Querier returns the ambient transaction when there is one, otherwise the pool.
func (m *TxManager) Querier(ctx context.Context) Querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return m.pool
}

func isTxClosed(err error) bool {
	return err != nil && err.Error() == pgx.ErrTxClosed.Error()
}
