package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := CORS([]string{"https://shop.example"})(ok)

	tests := []struct {
		name, method, origin string
		preflight            bool
		wantStatus           int
		wantAllow            string
	}{
		{"allowed origin", http.MethodGet, "https://shop.example", false, 200, "https://shop.example"},
		{"other origin", http.MethodGet, "https://evil.example", false, 200, ""},
		{"same origin (no header)", http.MethodGet, "", false, 200, ""},
		{"preflight", http.MethodOptions, "https://shop.example", true, 204, "https://shop.example"},
		{"preflight from other origin", http.MethodOptions, "https://evil.example", true, 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/api/v1/products", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.preflight {
				r.Header.Set("Access-Control-Request-Method", "POST")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.wantStatus || rec.Header().Get("Access-Control-Allow-Origin") != tt.wantAllow {
				t.Errorf("status %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
			}
			if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Error("credentials must never be allowed")
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
}
