package app_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestStorefrontPages(t *testing.T) {
	ta := newTestApp(t)

	// Every page a user can land on serves the app, including the links in
	// password-reset emails and Stripe's redirects.
	for _, path := range []string{"/", "/reset-password?token=abc", "/checkout/success?order_id=x", "/checkout/cancel?order_id=x"} {
		rec := ta.do(http.MethodGet, path, nil, "")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: CSP = %q", path, csp)
		}
	}

	js := ta.do(http.MethodGet, "/assets/app.js", nil, "")
	if js.Code != http.StatusOK || !strings.Contains(js.Header().Get("Content-Type"), "javascript") {
		t.Errorf("app.js: %d %s", js.Code, js.Header().Get("Content-Type"))
	}
	// Unknown paths still get the JSON error envelope.
	expectError(t, ta.do(http.MethodGet, "/admin.php", nil, ""), http.StatusNotFound, "ROUTE_NOT_FOUND")
}

func TestSecurityHeadersOnAPIResponses(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.do(http.MethodGet, "/api/v1/products", nil, "")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	// No CORS headers unless origins are configured.
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS enabled without configuration")
	}
}
