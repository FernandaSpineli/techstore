// Package config loads application settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Env identifies the environment the application is running in.
type Env string

const (
	EnvDevelopment Env = "development"
	EnvTest        Env = "test"
	EnvProduction  Env = "production"
)

// Config holds every setting the application reads at startup.
type Config struct {
	Env             Env
	HTTPAddr        string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	DatabaseURL     Secret
	RedisURL        Secret
}

// Load builds a Config from getenv (usually os.Getenv). It reports every
// invalid variable at once so a misconfigured deploy fails with a single,
// complete message.
func Load(getenv func(string) string) (Config, error) {
	var errs []error

	cfg := Config{
		Env:         Env(lookup(getenv, "APP_ENV", string(EnvDevelopment))),
		HTTPAddr:    lookup(getenv, "HTTP_ADDR", ":8080"),
		DatabaseURL: Secret(getenv("DATABASE_URL")),
		RedisURL:    Secret(getenv("REDIS_URL")),
	}

	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required"))
	}
	if cfg.RedisURL == "" {
		errs = append(errs, errors.New("REDIS_URL: required"))
	}

	switch cfg.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV: unknown environment %q", cfg.Env))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(lookup(getenv, "LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	var err error
	if cfg.ShutdownTimeout, err = parseDuration(getenv, "SHUTDOWN_TIMEOUT", "15s"); err != nil {
		errs = append(errs, err)
	}

	return cfg, errors.Join(errs...)
}

func lookup(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(getenv func(string) string, key, fallback string) (time.Duration, error) {
	d, err := time.ParseDuration(lookup(getenv, key, fallback))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive", key)
	}
	return d, nil
}
