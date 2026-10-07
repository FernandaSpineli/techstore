package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

type envelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return e
}

func TestWriteErrorExposesAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil),
		NewError(http.StatusConflict, "INSUFFICIENT_STOCK", "Not enough stock"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := decode(t, rec).Error; got.Code != "INSUFFICIENT_STOCK" || got.Message != "Not enough stock" {
		t.Errorf("unexpected error body: %+v", got)
	}
}

func TestWriteErrorHidesInternalErrors(t *testing.T) {
	var logs bytes.Buffer
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(logging.WithLogger(r.Context(), logging.New(&logs, slog.LevelInfo)))
	rec := httptest.NewRecorder()

	WriteError(rec, r, errors.New("pq: connection refused to 10.0.0.5"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Error("internal error detail leaked to the client")
	}
	if !strings.Contains(logs.String(), "10.0.0.5") {
		t.Error("internal error detail was not logged")
	}
}

func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{name: "generated when missing", incoming: "", keep: false},
		{name: "propagated when valid", incoming: "abc-123_DEF", keep: true},
		{name: "replaced when it has unsafe characters", incoming: "x\n{\"level\":\"error\"}", keep: false},
		{name: "replaced when too long", incoming: strings.Repeat("a", 65), keep: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = RequestIDFrom(r.Context())
			}))
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("X-Request-ID", tt.incoming)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if seen == "" || seen != rec.Header().Get("X-Request-ID") {
				t.Fatalf("context id %q, header %q", seen, rec.Header().Get("X-Request-ID"))
			}
			if (seen == tt.incoming) != tt.keep {
				t.Errorf("id = %q, keep incoming = %v", seen, tt.keep)
			}
		})
	}
}

func TestAccessLogRecordsRoutePatternAndStatus(t *testing.T) {
	var logs bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := Chain(mux, RequestID, AccessLog(logging.New(&logs, slog.LevelInfo)))

	r := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	r.Header.Set("Authorization", "Bearer super-secret-token")
	h.ServeHTTP(httptest.NewRecorder(), r)

	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if line["event"] != "http.request" || line["route"] != "GET /items/{id}" || line["status"] != float64(http.StatusTeapot) {
		t.Errorf("unexpected access log: %v", line)
	}
	if line["request_id"] == "" {
		t.Error("access log is missing request_id")
	}
	if strings.Contains(logs.String(), "super-secret-token") {
		t.Error("authorization header leaked into logs")
	}
}

func TestRecoverReturns500WithoutStackTrace(t *testing.T) {
	var logs bytes.Buffer
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: secret internals")
	}), RequestID, AccessLog(logging.New(&logs, slog.LevelInfo)), Recover)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret internals") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Error("panic details leaked to the client")
	}
	if got := decode(t, rec).Error.Code; got != "INTERNAL" {
		t.Errorf("code = %q, want INTERNAL", got)
	}
	if !strings.Contains(logs.String(), "http.panic") || !strings.Contains(logs.String(), "secret internals") {
		t.Error("panic was not logged")
	}
}
