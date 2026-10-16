package pgmigrate_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgmigrate"
	"github.com/avito-kitchen/platform/test/testenv"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLoadOrdersByVersionAndSkipsOtherFiles(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"db/10_third.sql":   {Data: []byte("SELECT 10;")},
		"db/2_second.sql":   {Data: []byte("SELECT 2;")},
		"db/1_first.sql":    {Data: []byte("SELECT 1;")},
		"db/README.md":      {Data: []byte("not a migration")},
		"db/nested/3_x.sql": {Data: []byte("SELECT 3;")},
	}
	got, err := pgmigrate.Load(fsys, "db")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d migrations, want 3: %+v", len(got), got)
	}
	for i, want := range []struct {
		version int64
		name    string
	}{{1, "first"}, {2, "second"}, {10, "third"}} {
		if got[i].Version != want.version || got[i].Name != want.name {
			t.Fatalf("migration %d = %d_%s, want %d_%s", i, got[i].Version, got[i].Name, want.version, want.name)
		}
	}
}

func TestLoadRefusesBrokenSets(t *testing.T) {
	t.Parallel()
	cases := map[string]fstest.MapFS{
		"no version":      {"db/init.sql": {Data: []byte("SELECT 1;")}},
		"text version":    {"db/x_init.sql": {Data: []byte("SELECT 1;")}},
		"duplicate":       {"db/1_a.sql": {Data: []byte("SELECT 1;")}, "db/001_b.sql": {Data: []byte("SELECT 1;")}},
		"empty file":      {"db/1_a.sql": {Data: []byte("  \n ")}},
		"nothing to load": {"db/README.md": {Data: []byte("x")}},
	}
	for name, fsys := range cases {
		if _, err := pgmigrate.Load(fsys, "db"); err == nil {
			t.Errorf("%s: Load() = nil error, want a refusal", name)
		}
	}
	if _, err := pgmigrate.Load(fstest.MapFS{}, "missing"); err == nil {
		t.Error("a missing directory must be reported")
	}
}

// The committed migrations themselves must load: a malformed file name would
// otherwise surface only when the migrate job runs in Compose.
func TestCommittedMigrationsLoad(t *testing.T) {
	t.Parallel()
	for _, dir := range migrations.Known {
		files, err := pgmigrate.Load(migrations.FS, dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if files[0].Version != 1 {
			t.Fatalf("%s starts at version %d, want 1", dir, files[0].Version)
		}
	}
}

// Apply records every version once, skips what is applied and leaves no
// trace of a failing migration.
func TestApplyIsIdempotentAndTransactional(t *testing.T) {
	pool := testenv.RequireDB(t, migrations.DirMerchant)
	ctx := testCtx(t)
	base, err := pgmigrate.Status(ctx, pool)
	if err != nil {
		t.Fatalf("Status() = %v", err)
	}

	extra := []pgmigrate.Migration{
		{Version: 1001, Name: "extra", SQL: `CREATE TABLE pgmigrate_extra (id integer PRIMARY KEY);
INSERT INTO pgmigrate_extra (id) VALUES (1);`},
	}
	for range 2 {
		if err := pgmigrate.Apply(ctx, pool, quietLogger(), extra); err != nil {
			t.Fatalf("Apply() = %v", err)
		}
	}
	if got, _ := pgmigrate.Status(ctx, pool); got != 1001 || base >= 1001 {
		t.Fatalf("status = %d (base %d), want 1001", got, base)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pgmigrate_extra`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("the migration ran %d times (%v), want once", rows, err)
	}

	// The second statement fails, so the first one must be rolled back too.
	broken := []pgmigrate.Migration{extra[0], {Version: 1002, Name: "broken", SQL: `
CREATE TABLE pgmigrate_partial (id integer);
INSERT INTO no_such_table VALUES (1);`}}
	err = pgmigrate.Apply(ctx, pool, quietLogger(), broken)
	if err == nil || !strings.Contains(err.Error(), "1002_broken") {
		t.Fatalf("Apply(broken) = %v, want the failing version named", err)
	}
	if got, _ := pgmigrate.Status(ctx, pool); got != 1001 {
		t.Fatalf("status after a failure = %d, want 1001", got)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('pgmigrate_partial') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("a failed migration left its first statement behind (%v)", err)
	}
}

// Concurrent runners are serialised by the advisory lock: each version is
// applied exactly once.
func TestConcurrentApplyRunsEachVersionOnce(t *testing.T) {
	pool := testenv.RequireDB(t, migrations.DirMerchant)
	ctx := testCtx(t)
	set := []pgmigrate.Migration{{Version: 2001, Name: "once", SQL: `
CREATE TABLE pgmigrate_once (id integer PRIMARY KEY);
INSERT INTO pgmigrate_once (id) VALUES (1);`}}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- pgmigrate.Apply(ctx, pool, quietLogger(), set)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Apply() = %v", err)
		}
	}
	var recorded int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations WHERE version = 2001`).Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("version 2001 recorded %d times (%v), want once", recorded, err)
	}
}
