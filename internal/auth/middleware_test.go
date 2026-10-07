package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
)

func TestAuthenticateAndRequireRole(t *testing.T) {
	svc := &Service{tokens: &tokenIssuer{secret: testSecret, ttl: time.Minute, now: time.Now}}
	customer, _ := svc.tokens.issue("u-customer", RoleCustomer)
	admin, _ := svc.tokens.issue("u-admin", RoleAdmin)

	var seen Principal
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	signedIn := Authenticate(svc)(ok)
	adminOnly := httpx.Chain(ok, Authenticate(svc), RequireRole(RoleAdmin))

	tests := []struct {
		name    string
		handler http.Handler
		header  string
		want    int
		wantID  string
	}{
		{"no header", signedIn, "", http.StatusUnauthorized, ""},
		{"wrong scheme", signedIn, "Basic " + customer, http.StatusUnauthorized, ""},
		{"garbage token", signedIn, "Bearer not-a-token", http.StatusUnauthorized, ""},
		{"valid token", signedIn, "Bearer " + customer, http.StatusNoContent, "u-customer"},
		{"scheme is case-insensitive", signedIn, "bearer " + customer, http.StatusNoContent, "u-customer"},
		{"customer on admin route", adminOnly, "Bearer " + customer, http.StatusForbidden, ""},
		{"admin on admin route", adminOnly, "Bearer " + admin, http.StatusNoContent, "u-admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen = Principal{}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, r)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if seen.UserID != tt.wantID {
				t.Errorf("handler saw user %q, want %q", seen.UserID, tt.wantID)
			}
			if tt.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate header")
			}
		})
	}
}
