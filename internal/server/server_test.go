package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

func TestHealthz(t *testing.T) {
	h := NewHandler(Deps{Logger: logging.New(&bytes.Buffer{}, slog.LevelInfo)})

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
	h := NewHandler(Deps{Logger: logging.New(&bytes.Buffer{}, slog.LevelInfo)})

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
