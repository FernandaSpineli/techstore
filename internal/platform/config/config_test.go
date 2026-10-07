package config

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

// env returns a getenv func with the required variables set, plus overrides.
func env(overrides map[string]string) func(string) string {
	vars := map[string]string{
		"DATABASE_URL": "postgres://localhost/techstore",
		"REDIS_URL":    "redis://localhost:6379/0",
		"JWT_SECRET":   "0123456789abcdef0123456789abcdef",
	}
	for k, v := range overrides {
		vars[k] = v
	}
	return func(key string) string { return vars[key] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != EnvDevelopment {
		t.Errorf("Env = %q, want %q", cfg.Env, EnvDevelopment)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want INFO", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
	if cfg.AccessTokenTTL != 15*time.Minute || cfg.RefreshTokenTTL != 720*time.Hour || cfg.PasswordResetTTL != 30*time.Minute {
		t.Errorf("unexpected token TTLs: %v %v %v", cfg.AccessTokenTTL, cfg.RefreshTokenTTL, cfg.PasswordResetTTL)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"APP_ENV":               "production",
		"STRIPE_SECRET_KEY":     "sk_live_example",
		"STRIPE_WEBHOOK_SECRET": "whsec_example",
		"HTTP_ADDR":             ":9000",
		"LOG_LEVEL":             "debug",
		"SHUTDOWN_TIMEOUT":      "3s",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != EnvProduction || cfg.HTTPAddr != ":9000" || cfg.LogLevel != slog.LevelDebug || cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadReportsAllInvalidVariables(t *testing.T) {
	_, err := Load(env(map[string]string{
		"APP_ENV":          "staging",
		"LOG_LEVEL":        "loud",
		"SHUTDOWN_TIMEOUT": "-1s",
		"DATABASE_URL":     "",
		"REDIS_URL":        "",
		"JWT_SECRET":       "too-short",
		"ACCESS_TOKEN_TTL": "soon",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	for _, key := range []string{"APP_ENV", "LOG_LEVEL", "SHUTDOWN_TIMEOUT", "DATABASE_URL", "REDIS_URL", "JWT_SECRET", "ACCESS_TOKEN_TTL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not mention %s", err, key)
		}
	}
}

func TestLoadStripeKeys(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantErr string
	}{
		{"not configured in development", map[string]string{}, ""},
		{"test keys", map[string]string{"STRIPE_SECRET_KEY": "sk_test_123", "STRIPE_WEBHOOK_SECRET": "whsec_123"}, ""},
		{"required in production", map[string]string{"APP_ENV": "production"}, "required in production"},
		{"only one of the pair", map[string]string{"STRIPE_SECRET_KEY": "sk_test_123"}, "set together"},
		{"publishable key by mistake", map[string]string{"STRIPE_SECRET_KEY": "pk_test_123", "STRIPE_WEBHOOK_SECRET": "whsec_1"}, "secret or restricted key"},
		{"live key outside production", map[string]string{"STRIPE_SECRET_KEY": "sk_live_123", "STRIPE_WEBHOOK_SECRET": "whsec_1"}, "only accepted when APP_ENV=production"},
		{"live key in production", map[string]string{"APP_ENV": "production", "STRIPE_SECRET_KEY": "sk_live_123", "STRIPE_WEBHOOK_SECRET": "whsec_1"}, ""},
		{"bad webhook secret", map[string]string{"STRIPE_SECRET_KEY": "sk_test_123", "STRIPE_WEBHOOK_SECRET": "secret"}, "whsec_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.vars))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Load() error = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Load() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseOrigins(t *testing.T) {
	cfg, err := Load(env(map[string]string{"CORS_ALLOWED_ORIGINS": " https://a.example/, ,https://b.example "}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.CORSAllowedOrigins, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("CORSAllowedOrigins = %v", cfg.CORSAllowedOrigins)
	}
}
