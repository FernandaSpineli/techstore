package auth

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
)

// Handler exposes the auth use cases over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

type authResponse struct {
	User   User   `json:"user"`
	Tokens Tokens `json:"tokens"`
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// Register handles POST /api/v1/auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) error {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	req.Email, req.Name = normalizeEmail(req.Email), strings.TrimSpace(req.Name)

	var p httpx.Problems
	validateEmail(&p, "email", req.Email)
	validateName(&p, "name", req.Name)
	if msg := validatePassword(req.Password); msg != "" {
		p.Add("password", msg)
	}
	if err := p.Err(); err != nil {
		return err
	}

	user, tokens, err := h.svc.Register(r.Context(), req.Email, req.Name, req.Password)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusCreated, authResponse{User: user, Tokens: tokens})
	return nil
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login handles POST /api/v1/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var p httpx.Problems
	if req.Email == "" {
		p.Add("email", "is required")
	}
	if req.Password == "" {
		p.Add("password", "is required")
	}
	if err := p.Err(); err != nil {
		return err
	}

	user, tokens, err := h.svc.Login(r.Context(), normalizeEmail(req.Email), req.Password)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, authResponse{User: user, Tokens: tokens})
	return nil
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func decodeRefreshToken(w http.ResponseWriter, r *http.Request) (string, error) {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return "", err
	}
	if req.RefreshToken == "" {
		var p httpx.Problems
		p.Add("refresh_token", "is required")
		return "", p.Err()
	}
	return req.RefreshToken, nil
}

// Refresh handles POST /api/v1/auth/refresh.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) error {
	token, err := decodeRefreshToken(w, r)
	if err != nil {
		return err
	}
	tokens, err := h.svc.Refresh(r.Context(), token)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, tokens)
	return nil
}

// Logout handles POST /api/v1/auth/logout.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) error {
	token, err := decodeRefreshToken(w, r)
	if err != nil {
		return err
	}
	if err := h.svc.Logout(r.Context(), token); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

// ForgotPassword handles POST /api/v1/auth/password/forgot. It always
// answers 202 so it cannot be used to discover registered emails.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) error {
	var req forgotPasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	req.Email = normalizeEmail(req.Email)
	var p httpx.Problems
	validateEmail(&p, "email", req.Email)
	if err := p.Err(); err != nil {
		return err
	}

	if err := h.svc.RequestPasswordReset(r.Context(), req.Email); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{
		"message": "If the email is registered, a reset link has been sent.",
	})
	return nil
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ResetPassword handles POST /api/v1/auth/password/reset.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) error {
	var req resetPasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var p httpx.Problems
	if req.Token == "" {
		p.Add("token", "is required")
	}
	if msg := validatePassword(req.NewPassword); msg != "" {
		p.Add("new_password", msg)
	}
	if err := p.Err(); err != nil {
		return err
	}

	if err := h.svc.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		return toHTTPError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword handles PUT /api/v1/users/me/password. It requires the
// authentication middleware.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) error {
	principal, _ := PrincipalFrom(r.Context())

	var req changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var p httpx.Problems
	if req.CurrentPassword == "" {
		p.Add("current_password", "is required")
	}
	if msg := validatePassword(req.NewPassword); msg != "" {
		p.Add("new_password", msg)
	}
	if err := p.Err(); err != nil {
		return err
	}

	err := h.svc.ChangePassword(r.Context(), principal.UserID, req.CurrentPassword, req.NewPassword)
	if errors.Is(err, ErrInvalidCredentials) {
		// 422 rather than 401: the caller is authenticated, and a 401 would
		// make most clients drop the session.
		p.Add("current_password", "is incorrect")
		return p.Err()
	}
	if err != nil {
		return toHTTPError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func toHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrEmailTaken):
		return httpx.NewError(http.StatusConflict, "EMAIL_TAKEN", "An account with this email already exists")
	case errors.Is(err, ErrInvalidCredentials):
		return httpx.NewError(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password")
	case errors.Is(err, ErrInvalidRefreshToken):
		return httpx.NewError(http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh token is invalid or expired")
	case errors.Is(err, ErrInvalidResetToken):
		return httpx.NewError(http.StatusBadRequest, "INVALID_RESET_TOKEN", "Reset token is invalid or expired")
	case errors.Is(err, ErrUserNotFound):
		return httpx.NewError(http.StatusNotFound, "USER_NOT_FOUND", "User not found")
	}
	return err
}

// normalizeEmail trims and lower-cases an email. The column is citext, so
// this is for consistent storage and display, not for uniqueness.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validateEmail records a problem if email is not a bare address.
func validateEmail(p *httpx.Problems, field, email string) {
	addr, err := mail.ParseAddress(email)
	switch {
	case email == "":
		p.Add(field, "is required")
	case len(email) > 254 || err != nil || addr.Address != email:
		p.Add(field, "must be a valid email address")
	}
}

func validateName(p *httpx.Problems, field, name string) {
	if n := utf8.RuneCountInString(name); n < 1 || n > 120 {
		p.Add(field, "must be between 1 and 120 characters")
	}
}
