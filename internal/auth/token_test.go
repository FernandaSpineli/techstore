package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("test-secret-test-secret-test-secret")

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestTokenRoundTrip(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ti := &tokenIssuer{secret: testSecret, ttl: 15 * time.Minute, now: fixedClock(now)}

	token, err := ti.issue("user-1", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ti.parse(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.UserID != "user-1" || p.Role != RoleAdmin {
		t.Errorf("principal = %+v", p)
	}
}

func TestTokenRejected(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ti := &tokenIssuer{secret: testSecret, ttl: 15 * time.Minute, now: fixedClock(now)}
	valid, _ := ti.issue("user-1", RoleCustomer)

	sign := func(method jwt.SigningMethod, key any, c accessClaims) string {
		s, err := jwt.NewWithClaims(method, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	claims := func(mut func(*accessClaims)) accessClaims {
		c := accessClaims{Role: RoleCustomer, RegisteredClaims: jwt.RegisteredClaims{
			Issuer: jwtIssuer, Subject: "user-1", Audience: jwt.ClaimStrings{jwtAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		}}
		mut(&c)
		return c
	}
	parts := strings.Split(valid, ".")

	tests := map[string]string{
		"expired":          sign(jwt.SigningMethodHS256, testSecret, claims(func(c *accessClaims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Second)) })),
		"no expiry":        sign(jwt.SigningMethodHS256, testSecret, claims(func(c *accessClaims) { c.ExpiresAt = nil })),
		"wrong secret":     sign(jwt.SigningMethodHS256, []byte("another-secret-another-secret-xx"), claims(func(*accessClaims) {})),
		"alg none":         sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims(func(*accessClaims) {})),
		"other algorithm":  sign(jwt.SigningMethodHS512, testSecret, claims(func(*accessClaims) {})),
		"wrong issuer":     sign(jwt.SigningMethodHS256, testSecret, claims(func(c *accessClaims) { c.Issuer = "someone-else" })),
		"wrong audience":   sign(jwt.SigningMethodHS256, testSecret, claims(func(c *accessClaims) { c.Audience = jwt.ClaimStrings{"other-api"} })),
		"unknown role":     sign(jwt.SigningMethodHS256, testSecret, claims(func(c *accessClaims) { c.Role = "superuser" })),
		"missing subject":  sign(jwt.SigningMethodHS256, testSecret, claims(func(c *accessClaims) { c.Subject = "" })),
		"tampered payload": parts[0] + "." + strings.Repeat("A", len(parts[1])) + "." + parts[2],
		"not a jwt":        "hello",
		"empty":            "",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			if p, err := ti.parse(token); err == nil {
				t.Fatalf("parse accepted token, principal = %+v", p)
			}
		})
	}
}
