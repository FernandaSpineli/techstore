// Package pgtest gives integration tests an isolated, fully migrated
// PostgreSQL database.
//
// One container is started per test binary. The schema is migrated once into
// a template database, and every call to New clones it with
// CREATE DATABASE ... TEMPLATE, which takes milliseconds. Tests therefore run
// against the real schema without sharing state.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// Image is the PostgreSQL image used in tests; keep it in sync with
// docker-compose.yml.
const Image = "postgres:18-alpine"

const templateDB = "techstore_template"

var (
	once     sync.Once
	adminURL string // connects to the maintenance "postgres" database
	setupErr error
)

// New returns a pool connected to a fresh, migrated database that is dropped
// when the test ends. It skips the test in -short mode.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (skipped with -short)")
	}

	once.Do(func() { setupErr = setup() })
	if setupErr != nil {
		t.Fatalf("pgtest: setup: %v", setupErr)
	}

	ctx := context.Background()
	name := "test_" + randomSuffix()
	// Identifiers cannot be bound as parameters; name is generated above.
	if err := exec(ctx, adminURL, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("pgtest: parse url: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgtest: connect: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if err := exec(context.Background(), adminURL, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name)); err != nil {
			t.Errorf("pgtest: drop database: %v", err)
		}
	})
	return pool
}

// setup starts the container and migrates the template database. The
// container is removed by testcontainers' reaper when the test binary exits.
func setup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ctr, err := tcpostgres.Run(ctx, Image,
		tcpostgres.WithDatabase(templateDB),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
		// Durability is irrelevant for throwaway test data; trade it for speed.
		testcontainers.WithCmd("postgres", "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"),
	)
	if err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	templateURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("connection string: %w", err)
	}

	pool, err := postgres.Connect(ctx, templateURL)
	if err != nil {
		return err
	}
	_, err = postgres.MigrateUp(ctx, pool)
	// A template must have no open connections before it can be cloned.
	pool.Close()
	if err != nil {
		return err
	}

	cfg, err := pgx.ParseConfig(templateURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	adminURL = fmt.Sprintf("postgres://test:test@%s:%d/postgres?sslmode=disable", cfg.Host, cfg.Port)
	return nil
}

func exec(ctx context.Context, url, sql string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
