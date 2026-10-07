package postgres_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres/pgtest"
	"github.com/FernandaSpineli/techstore/migrations"
)

// TestMigrationsRoundTrip proves every Down migration reverses its Up: the
// schema can be torn down to nothing and rebuilt.
func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t) // already migrated to the latest version

	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	latest := int64(len(files))

	m, err := postgres.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if v, err := m.GetDBVersion(ctx); err != nil || v != latest {
		t.Fatalf("version = %d, %v; want %d", v, err, latest)
	}

	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	var tables int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`,
	).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("%d tables left after migrating down to 0", tables)
	}

	results, err := m.Up(ctx)
	if err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if int64(len(results)) != latest {
		t.Fatalf("applied %d migrations, want %d", len(results), latest)
	}

	results, err = m.Up(ctx)
	if err != nil || len(results) != 0 {
		t.Fatalf("second up applied %d migrations (err %v), want 0", len(results), err)
	}
}
