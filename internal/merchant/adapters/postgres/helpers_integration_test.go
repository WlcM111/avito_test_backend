package postgres_test

import (
	"context"
	"io"
	"log/slog"

	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgmigrate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pgmigrateStatus(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	return pgmigrate.Status(ctx, pool)
}

func applyAgain(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := pgmigrate.Load(migrations.FS, migrations.DirMerchant)
	if err != nil {
		return err
	}
	return pgmigrate.Apply(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil)), files)
}
