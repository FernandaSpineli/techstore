package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/FernandaSpineli/techstore/internal/platform/config"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// newHandler builds the app without a database, enough for routes that do
// not touch it.
func newHandler(t *testing.T, checks map[string]func(context.Context) error) http.Handler {
	t.Helper()
	a, err := New(Deps{
		Config:          config.Config{JWTSecret: "test-secret-test-secret-test-secret"},
		Logger:          logging.New(&bytes.Buffer{}, slog.LevelInfo),
		ReadinessChecks: checks,
		BcryptCost:      bcrypt.MinCost,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.Handler
}

func TestHealthz(t *testing.T) {
	h := newHandler(t, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID header")
	}
}

func TestUnknownRouteUsesErrorEnvelope(t *testing.T) {
	h := newHandler(t, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Error.Code != "ROUTE_NOT_FOUND" {
		t.Errorf("code = %q, want ROUTE_NOT_FOUND", body.Error.Code)
	}
	if body.Error.RequestID != rec.Header().Get("X-Request-ID") {
		t.Errorf("request_id %q does not match header", body.Error.RequestID)
	}
}

func TestReadyz(t *testing.T) {
	ok := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("dial tcp 10.0.0.7:5432: connection refused") }

	tests := []struct {
		name       string
		checks     map[string]func(context.Context) error
		wantStatus int
		wantChecks map[string]string
	}{
		{
			name:       "all dependencies up",
			checks:     map[string]func(context.Context) error{"postgres": ok, "redis": ok},
			wantStatus: http.StatusOK,
			wantChecks: map[string]string{"postgres": "ok", "redis": "ok"},
		},
		{
			name:       "one dependency down",
			checks:     map[string]func(context.Context) error{"postgres": down, "redis": ok},
			wantStatus: http.StatusServiceUnavailable,
			wantChecks: map[string]string{"postgres": "unavailable", "redis": "ok"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t, tt.checks)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var body struct {
				Checks map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			for dep, want := range tt.wantChecks {
				if body.Checks[dep] != want {
					t.Errorf("checks[%s] = %q, want %q", dep, body.Checks[dep], want)
				}
			}
			if strings.Contains(rec.Body.String(), "10.0.0.7") {
				t.Error("readiness response leaked infrastructure details")
			}
		})
	}
}
