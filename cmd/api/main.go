// Command api runs the TechStore HTTP API.
//
// Usage:
//
//	api                         serve the HTTP API
//	api migrate up|down|status  manage the database schema
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/FernandaSpineli/techstore/internal/platform/config"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
	"github.com/FernandaSpineli/techstore/internal/platform/redisx"
	"github.com/FernandaSpineli/techstore/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "techstore: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := logging.New(stdout, cfg.LogLevel).With("service", "techstore", "env", string(cfg.Env))
	slog.SetDefault(logger)

	db, err := postgres.Connect(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer db.Close()

	switch {
	case len(args) == 0:
		return serve(ctx, cfg, logger, db)
	case args[0] == "migrate" && len(args) == 2:
		return migrate(ctx, logger, db, args[1])
	default:
		return errors.New("usage: api [migrate up|down|status]")
	}
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, db *pgxpool.Pool) error {
	redisx.SetLogger(logger)
	rdb, err := redisx.Connect(ctx, cfg.RedisURL.Reveal())
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	handler := server.NewHandler(server.Deps{
		Logger: logger,
		ReadinessChecks: map[string]func(context.Context) error{
			"postgres": db.Ping,
			"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		},
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http.server.started", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("http.server.stopping", "timeout", cfg.ShutdownTimeout.String())
	// ctx is already cancelled here; keep its values but give shutdown its own deadline.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("http.server.stopped")
	return nil
}

func migrate(ctx context.Context, logger *slog.Logger, db *pgxpool.Pool, command string) error {
	m, err := postgres.NewMigrator(db)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	logResult := func(r *goose.MigrationResult) {
		logger.Info("db.migration.applied",
			"version", r.Source.Version,
			"file", path.Base(r.Source.Path),
			"direction", r.Direction,
			"duration_ms", r.Duration.Milliseconds())
	}

	switch command {
	case "up":
		results, err := m.Up(ctx)
		for _, r := range results {
			logResult(r)
		}
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		if len(results) == 0 {
			logger.Info("db.migration.up_to_date")
		}
	case "down":
		r, err := m.Down(ctx)
		if err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		logResult(r)
	case "status":
		statuses, err := m.Status(ctx)
		if err != nil {
			return fmt.Errorf("migrate status: %w", err)
		}
		for _, s := range statuses {
			logger.Info("db.migration.status",
				"version", s.Source.Version,
				"file", path.Base(s.Source.Path),
				"state", string(s.State))
		}
	default:
		return fmt.Errorf("unknown migrate command %q (want up, down or status)", command)
	}
	return nil
}
