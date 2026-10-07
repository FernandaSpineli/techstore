package httpx

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

	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		Qty  int    `json:"qty"`
	}
	tests := []struct {
		name     string
		body     string
		wantCode string
		status   int
	}{
		{name: "valid", body: `{"name":"x","qty":2}`},
		{name: "empty body", body: ``, wantCode: "INVALID_JSON", status: http.StatusBadRequest},
		{name: "syntax error", body: `{"name":`, wantCode: "INVALID_JSON", status: http.StatusBadRequest},
		{name: "wrong type", body: `{"qty":"two"}`, wantCode: "INVALID_JSON", status: http.StatusBadRequest},
		{name: "unknown field", body: `{"name":"x","admin":true}`, wantCode: "INVALID_JSON", status: http.StatusBadRequest},
		{name: "trailing object", body: `{"name":"x"}{"name":"y"}`, wantCode: "INVALID_JSON", status: http.StatusBadRequest},
		{name: "too large", body: `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, wantCode: "BODY_TOO_LARGE", status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst payload
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			err := DecodeJSON(httptest.NewRecorder(), r, &dst)

			if tt.wantCode == "" {
				if err != nil || dst.Name != "x" || dst.Qty != 2 {
					t.Fatalf("err = %v, dst = %+v", err, dst)
				}
				return
			}
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != tt.wantCode || apiErr.Status != tt.status {
				t.Fatalf("err = %v, want %s (%d)", err, tt.wantCode, tt.status)
			}
		})
	}
}

func TestProblems(t *testing.T) {
	var p Problems
	if p.Err() != nil {
		t.Fatal("empty Problems returned an error")
	}
	p.Add("email", "is required")
	p.Add("name", "is too long")

	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodPost, "/", nil), p.Err())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details []FieldProblem `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "VALIDATION_FAILED" || len(body.Error.Details) != 2 || body.Error.Details[0].Field != "email" {
		t.Errorf("unexpected body: %s", rec.Body)
	}
}

func TestAddAccessLogAttrs(t *testing.T) {
	var logs bytes.Buffer
	h := AccessLog(logging.New(&logs, slog.LevelInfo))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		AddAccessLogAttrs(r.Context(), slog.String("user_id", "u-1"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["user_id"] != "u-1" {
		t.Errorf("access log = %v, want user_id u-1", line)
	}

	// Outside of AccessLog it is a no-op rather than a panic.
	AddAccessLogAttrs(context.Background(), slog.String("k", "v"))
}
