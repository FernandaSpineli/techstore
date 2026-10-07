// Package config loads application settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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

	// BaseURL is the public URL of the shop, used to build links in emails.
	BaseURL string

	JWTSecret        Secret
	AccessTokenTTL   time.Duration
	RefreshTokenTTL  time.Duration
	PasswordResetTTL time.Duration

	// OrderReservationTTL is how long stock stays reserved for an unpaid order.
	OrderReservationTTL time.Duration

	// Requests per minute per client IP: AuthRateLimit on sign-in, sign-up
	// and password endpoints, APIRateLimit on everything under /api.
	AuthRateLimit int
	APIRateLimit  int
	// CatalogCacheTTL bounds how stale a cached catalog response can be.
	CatalogCacheTTL time.Duration

	// Stripe is optional outside production: without keys the API runs and
	// checkout reports that payments are unavailable.
	StripeSecretKey     Secret
	StripeWebhookSecret Secret

	SMTPAddr     string
	SMTPUsername string
	SMTPPassword Secret
	MailFrom     string
}

// minJWTSecretLen is 32 bytes: HS256 keys shorter than the hash output
// weaken the signature.
const minJWTSecretLen = 32

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
		BaseURL:     lookup(getenv, "APP_BASE_URL", "http://localhost:8080"),

		JWTSecret: Secret(getenv("JWT_SECRET")),

		StripeSecretKey:     Secret(getenv("STRIPE_SECRET_KEY")),
		StripeWebhookSecret: Secret(getenv("STRIPE_WEBHOOK_SECRET")),

		SMTPAddr:     lookup(getenv, "SMTP_ADDR", "localhost:1025"),
		SMTPUsername: getenv("SMTP_USERNAME"),
		SMTPPassword: Secret(getenv("SMTP_PASSWORD")),
		MailFrom:     lookup(getenv, "MAIL_FROM", "TechStore <no-reply@techstore.local>"),
	}

	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required"))
	}
	if cfg.RedisURL == "" {
		errs = append(errs, errors.New("REDIS_URL: required"))
	}
	if len(cfg.JWTSecret) < minJWTSecretLen {
		errs = append(errs, fmt.Errorf("JWT_SECRET: must be at least %d characters", minJWTSecretLen))
	}

	switch cfg.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV: unknown environment %q", cfg.Env))
	}
	errs = append(errs, validateStripe(cfg)...)

	if err := cfg.LogLevel.UnmarshalText([]byte(lookup(getenv, "LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	for _, d := range []struct {
		dst      *time.Duration
		key, def string
	}{
		{&cfg.ShutdownTimeout, "SHUTDOWN_TIMEOUT", "15s"},
		{&cfg.AccessTokenTTL, "ACCESS_TOKEN_TTL", "15m"},
		{&cfg.RefreshTokenTTL, "REFRESH_TOKEN_TTL", "720h"},
		{&cfg.PasswordResetTTL, "PASSWORD_RESET_TTL", "30m"},
		{&cfg.OrderReservationTTL, "ORDER_RESERVATION_TTL", "30m"},
		{&cfg.CatalogCacheTTL, "CATALOG_CACHE_TTL", "60s"},
	} {
		v, err := parseDuration(getenv, d.key, d.def)
		if err != nil {
			errs = append(errs, err)
		}
		*d.dst = v
	}

	for _, n := range []struct {
		dst      *int
		key, def string
	}{
		{&cfg.AuthRateLimit, "RATE_LIMIT_AUTH_PER_MINUTE", "10"},
		{&cfg.APIRateLimit, "RATE_LIMIT_API_PER_MINUTE", "300"},
	} {
		v, err := strconv.Atoi(lookup(getenv, n.key, n.def))
		if err != nil || v < 1 {
			errs = append(errs, fmt.Errorf("%s: must be a positive integer", n.key))
		}
		*n.dst = v
	}

	return cfg, errors.Join(errs...)
}

func validateStripe(cfg Config) []error {
	key, hook := cfg.StripeSecretKey.Reveal(), cfg.StripeWebhookSecret.Reveal()
	var errs []error
	switch {
	case key == "" && hook == "":
		if cfg.Env == EnvProduction {
			errs = append(errs, errors.New("STRIPE_SECRET_KEY: required in production"))
		}
		return errs
	case key == "" || hook == "":
		return append(errs, errors.New("STRIPE_SECRET_KEY and STRIPE_WEBHOOK_SECRET must be set together"))
	}
	live := strings.HasPrefix(key, "sk_live_") || strings.HasPrefix(key, "rk_live_")
	test := strings.HasPrefix(key, "sk_test_") || strings.HasPrefix(key, "rk_test_")
	switch {
	case !live && !test:
		errs = append(errs, errors.New("STRIPE_SECRET_KEY: must be a Stripe secret or restricted key (sk_/rk_)"))
	case live && cfg.Env != EnvProduction:
		// A live key in a dev or test environment would move real money.
		errs = append(errs, errors.New("STRIPE_SECRET_KEY: live keys are only accepted when APP_ENV=production"))
	}
	if !strings.HasPrefix(hook, "whsec_") {
		errs = append(errs, errors.New("STRIPE_WEBHOOK_SECRET: must start with whsec_"))
	}
	return errs
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
