package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/FernandaSpineli/techstore/migrations"
)

// Migrator applies the embedded schema migrations. Goose takes a Postgres
// advisory lock, so concurrent runs (e.g. several replicas starting at once)
// are serialised.
type Migrator struct {
	*goose.Provider
	db *sql.DB
}

// NewMigrator returns a Migrator that runs over pool. Call Close when done.
func NewMigrator(pool *pgxpool.Pool) (*Migrator, error) {
	db := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: create migrator: %w", err)
	}
	return &Migrator{Provider: p, db: db}, nil
}

// Close releases the database/sql handle. The underlying pool stays open.
func (m *Migrator) Close() error { return m.db.Close() }

// MigrateUp applies all pending migrations.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationResult, error) {
	m, err := NewMigrator(pool)
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.Close() }()

	results, err := m.Up(ctx)
	if err != nil {
		return results, fmt.Errorf("postgres: migrate up: %w", err)
	}
	return results, nil
}
