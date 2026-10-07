// Package auth handles credentials: registration, login, access and refresh
// tokens, password changes and resets, and the HTTP middleware that
// authenticates and authorises requests.
package auth

import (
	"context"
	"errors"
	"time"
)

// Role is a user's authorisation level.
type Role string

const (
	RoleCustomer Role = "customer"
	RoleAdmin    Role = "admin"
)

func (r Role) valid() bool { return r == RoleCustomer || r == RoleAdmin }

// User is an account as seen by the auth module.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`

	passwordHash []byte
}

var (
	ErrEmailTaken          = errors.New("auth: email already registered")
	ErrInvalidCredentials  = errors.New("auth: invalid credentials")
	ErrInvalidRefreshToken = errors.New("auth: invalid refresh token")
	ErrInvalidResetToken   = errors.New("auth: invalid password reset token")
	ErrUserNotFound        = errors.New("auth: user not found")
)

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID string
	Role   Role
}

type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the authenticated caller. ok is false on routes that
// are not behind the authentication middleware.
func PrincipalFrom(ctx context.Context) (p Principal, ok bool) {
	p, ok = ctx.Value(principalKey{}).(Principal)
	return p, ok
}
