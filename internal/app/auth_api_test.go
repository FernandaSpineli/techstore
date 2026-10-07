package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

type user struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

type authResponse struct {
	User   user   `json:"user"`
	Tokens tokens `json:"tokens"`
}

func (ta *testApp) register(email, password string) authResponse {
	ta.t.Helper()
	return expect[authResponse](ta.t, ta.do(http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": email, "password": password, "name": "Test User"}, ""), http.StatusCreated)
}

func TestRegister(t *testing.T) {
	ta := newTestApp(t)

	rec := ta.do(http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": "  Ana@Example.com ", "password": "password123", "name": "Ana"}, "")
	got := expect[authResponse](t, rec, http.StatusCreated)

	if got.User.Email != "ana@example.com" || got.User.Role != "customer" || got.User.ID == "" {
		t.Errorf("unexpected user: %+v", got.User)
	}
	if got.Tokens.AccessToken == "" || got.Tokens.RefreshToken == "" || got.Tokens.TokenType != "Bearer" || got.Tokens.ExpiresIn != 900 {
		t.Errorf("unexpected tokens: %+v", got.Tokens)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Error("response exposes password data")
	}

	me := expect[user](t, ta.do(http.MethodGet, "/api/v1/users/me", nil, got.Tokens.AccessToken), http.StatusOK)
	if me.ID != got.User.ID {
		t.Errorf("me.ID = %q, want %q", me.ID, got.User.ID)
	}
}

func TestRegisterRejectsDuplicateEmailIgnoringCase(t *testing.T) {
	ta := newTestApp(t)
	ta.register("ana@example.com", "password123")

	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": "ANA@example.com", "password": "password123", "name": "Ana"}, ""),
		http.StatusConflict, "EMAIL_TAKEN")
}

func TestRegisterValidation(t *testing.T) {
	ta := newTestApp(t)

	e := expectError(t, ta.do(http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": "Ana <ana@example.com>", "password": "short", "name": " "}, ""),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")

	fields := map[string]bool{}
	for _, d := range e.problems(t) {
		fields[d.Field] = true
	}
	for _, f := range []string{"email", "password", "name"} {
		if !fields[f] {
			t.Errorf("no validation problem reported for %s: %s", f, e.Error.Details)
		}
	}
}

func TestRegisterRejectsMalformedBodies(t *testing.T) {
	ta := newTestApp(t)

	// A client must not be able to set its own role.
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": "ana@example.com", "password": "password123", "name": "Ana", "role": "admin"}, ""),
		http.StatusBadRequest, "INVALID_JSON")
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/register", nil, ""),
		http.StatusBadRequest, "INVALID_JSON")
}

func TestLogin(t *testing.T) {
	ta := newTestApp(t)
	ta.register("ana@example.com", "password123")

	got := expect[authResponse](t, ta.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "ANA@example.com", "password": "password123"}, ""), http.StatusOK)
	if got.Tokens.AccessToken == "" {
		t.Fatal("no access token")
	}

	wrongPassword := expectError(t, ta.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "ana@example.com", "password": "wrong-password"}, ""),
		http.StatusUnauthorized, "INVALID_CREDENTIALS")
	unknownEmail := expectError(t, ta.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "nobody@example.com", "password": "wrong-password"}, ""),
		http.StatusUnauthorized, "INVALID_CREDENTIALS")
	if wrongPassword.Error.Message != unknownEmail.Error.Message {
		t.Error("login errors differ between unknown email and wrong password")
	}
}

func TestProtectedRoutesRequireAValidToken(t *testing.T) {
	ta := newTestApp(t)

	rec := ta.do(http.MethodGet, "/api/v1/users/me", nil, "")
	expectError(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("missing WWW-Authenticate header")
	}
	expectError(t, ta.do(http.MethodGet, "/api/v1/users/me", nil, "forged.token.value"),
		http.StatusUnauthorized, "UNAUTHORIZED")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/users/me", map[string]string{"name": "x"}, ""),
		http.StatusUnauthorized, "UNAUTHORIZED")
}

func TestUpdateProfile(t *testing.T) {
	ta := newTestApp(t)
	auth := ta.register("ana@example.com", "password123")
	token := auth.Tokens.AccessToken

	got := expect[user](t, ta.do(http.MethodPatch, "/api/v1/users/me",
		map[string]string{"name": "  Ana Maria "}, token), http.StatusOK)
	if got.Name != "Ana Maria" {
		t.Errorf("name = %q, want %q", got.Name, "Ana Maria")
	}

	expectError(t, ta.do(http.MethodPatch, "/api/v1/users/me", map[string]string{"name": ""}, token),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	// Email and role are not editable through the profile.
	expectError(t, ta.do(http.MethodPatch, "/api/v1/users/me", map[string]string{"email": "evil@example.com"}, token),
		http.StatusBadRequest, "INVALID_JSON")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/users/me", map[string]string{"role": "admin"}, token),
		http.StatusBadRequest, "INVALID_JSON")
}

func TestRefreshRotatesTokensAndDetectsReuse(t *testing.T) {
	ta := newTestApp(t)
	first := ta.register("ana@example.com", "password123").Tokens

	second := expect[tokens](t, ta.do(http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": first.RefreshToken}, ""), http.StatusOK)
	if second.RefreshToken == first.RefreshToken || second.AccessToken == "" {
		t.Fatalf("refresh did not rotate tokens: %+v", second)
	}

	// Replaying the old token is treated as theft: it fails and also revokes
	// the session that was issued from it.
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": first.RefreshToken}, ""), http.StatusUnauthorized, "INVALID_REFRESH_TOKEN")
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": second.RefreshToken}, ""), http.StatusUnauthorized, "INVALID_REFRESH_TOKEN")

	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": "made-up"}, ""), http.StatusUnauthorized, "INVALID_REFRESH_TOKEN")
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	ta := newTestApp(t)
	tok := ta.register("ana@example.com", "password123").Tokens

	expect[any](t, ta.do(http.MethodPost, "/api/v1/auth/logout", map[string]string{"refresh_token": tok.RefreshToken}, ""), http.StatusNoContent)
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": tok.RefreshToken}, ""), http.StatusUnauthorized, "INVALID_REFRESH_TOKEN")

	// Unknown tokens are accepted silently: logout reveals nothing.
	expect[any](t, ta.do(http.MethodPost, "/api/v1/auth/logout", map[string]string{"refresh_token": "made-up"}, ""), http.StatusNoContent)
}

func TestChangePassword(t *testing.T) {
	ta := newTestApp(t)
	tok := ta.register("ana@example.com", "password123").Tokens

	e := expectError(t, ta.do(http.MethodPut, "/api/v1/users/me/password",
		map[string]string{"current_password": "wrong-password", "new_password": "new-password-1"}, tok.AccessToken),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	if ps := e.problems(t); len(ps) != 1 || ps[0].Field != "current_password" {
		t.Errorf("details = %+v, want current_password problem", ps)
	}

	expect[any](t, ta.do(http.MethodPut, "/api/v1/users/me/password",
		map[string]string{"current_password": "password123", "new_password": "new-password-1"}, tok.AccessToken),
		http.StatusNoContent)

	// Other sessions are signed out, the old password stops working.
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": tok.RefreshToken}, ""), http.StatusUnauthorized, "INVALID_REFRESH_TOKEN")
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "ana@example.com", "password": "password123"}, ""), http.StatusUnauthorized, "INVALID_CREDENTIALS")
	expect[authResponse](t, ta.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "ana@example.com", "password": "new-password-1"}, ""), http.StatusOK)
}

var resetLink = regexp.MustCompile(`http://shop\.test/reset-password\?token=(\S+)`)

func TestPasswordResetFlow(t *testing.T) {
	ta := newTestApp(t)
	tok := ta.register("ana@example.com", "password123").Tokens

	// Unknown emails get the same answer and no email is sent.
	unknown := ta.do(http.MethodPost, "/api/v1/auth/password/forgot", map[string]string{"email": "nobody@example.com"}, "")
	known := ta.do(http.MethodPost, "/api/v1/auth/password/forgot", map[string]string{"email": "ana@example.com"}, "")
	expect[any](t, unknown, http.StatusAccepted)
	expect[any](t, known, http.StatusAccepted)
	if unknown.Body.String() != known.Body.String() {
		t.Error("forgot-password responses differ for known and unknown emails")
	}

	ta.app.Wait() // the email is sent in the background
	msgs := ta.mailer.messages()
	if len(msgs) != 1 || msgs[0].To != "ana@example.com" {
		t.Fatalf("sent = %+v, want one email to ana@example.com", msgs)
	}
	m := resetLink.FindStringSubmatch(msgs[0].Body)
	if m == nil {
		t.Fatalf("no reset link in email body:\n%s", msgs[0].Body)
	}
	token, err := url.QueryUnescape(m[1])
	if err != nil {
		t.Fatal(err)
	}

	expect[any](t, ta.do(http.MethodPost, "/api/v1/auth/password/reset",
		map[string]string{"token": token, "new_password": "brand-new-pass"}, ""), http.StatusNoContent)

	// Single use.
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/password/reset",
		map[string]string{"token": token, "new_password": "another-pass-1"}, ""), http.StatusBadRequest, "INVALID_RESET_TOKEN")
	// Existing sessions are revoked.
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": tok.RefreshToken}, ""), http.StatusUnauthorized, "INVALID_REFRESH_TOKEN")
	expect[authResponse](t, ta.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "ana@example.com", "password": "brand-new-pass"}, ""), http.StatusOK)

	// The token must never reach the logs.
	if strings.Contains(ta.logs.String(), token) {
		t.Error("reset token was logged")
	}
}

func TestSecretsNeverReachLogs(t *testing.T) {
	ta := newTestApp(t)
	tok := ta.register("ana@example.com", "s3cret-passw0rd").Tokens
	ta.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "ana@example.com", "password": "wrong-passw0rd"}, "")
	ta.do(http.MethodGet, "/api/v1/users/me", nil, tok.AccessToken)
	ta.do(http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": tok.RefreshToken}, "")

	logs := ta.logs.String()
	for name, secret := range map[string]string{
		"password":       "s3cret-passw0rd",
		"wrong password": "wrong-passw0rd",
		"access token":   tok.AccessToken,
		"refresh token":  tok.RefreshToken,
		"email":          "ana@example.com",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("%s found in logs", name)
		}
	}
	if !strings.Contains(logs, `"event":"auth.login.failed"`) || !strings.Contains(logs, `"user_id"`) {
		t.Error("expected auth events with user_id in logs")
	}
}
