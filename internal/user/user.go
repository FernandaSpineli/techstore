// Package user serves the signed-in user's profile. Credentials (email,
// password, roles) are owned by the auth package.
package user

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
)

// Profile is the public view of the signed-in user.
type Profile struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Handler serves /api/v1/users/me. Every route requires authentication.
type Handler struct {
	db *pgxpool.Pool
}

// NewHandler returns a Handler.
func NewHandler(db *pgxpool.Pool) *Handler { return &Handler{db: db} }

var errNotFound = httpx.NewError(http.StatusNotFound, "USER_NOT_FOUND", "User not found")

const profileColumns = `id, email, name, role, created_at, updated_at`

func scanProfile(row pgx.Row) (Profile, error) {
	var p Profile
	err := row.Scan(&p.ID, &p.Email, &p.Name, &p.Role, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The token is valid but the account is gone.
		return Profile{}, errNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("user: scan profile: %w", err)
	}
	return p, nil
}

func (h *Handler) profile(ctx context.Context, id string) (Profile, error) {
	return scanProfile(h.db.QueryRow(ctx, `SELECT `+profileColumns+` FROM users WHERE id = $1`, id))
}

// Me handles GET /api/v1/users/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) error {
	principal, _ := auth.PrincipalFrom(r.Context())
	p, err := h.profile(r.Context(), principal.UserID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
	return nil
}

// updateRequest lists the editable profile fields. Email changes need a
// verified flow of their own and are deliberately not accepted here.
type updateRequest struct {
	Name *string `json:"name"`
}

// UpdateMe handles PATCH /api/v1/users/me.
func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) error {
	principal, _ := auth.PrincipalFrom(r.Context())

	var req updateRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var p httpx.Problems
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
		if n := utf8.RuneCountInString(*req.Name); n < 1 || n > 120 {
			p.Add("name", "must be between 1 and 120 characters")
		}
	}
	if err := p.Err(); err != nil {
		return err
	}

	profile, err := scanProfile(h.db.QueryRow(r.Context(), `
		UPDATE users SET name = COALESCE($2, name)
		WHERE id = $1
		RETURNING `+profileColumns, principal.UserID, req.Name))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, profile)
	return nil
}
