package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// store runs the auth module's SQL. It works on a pool or inside a
// transaction (see postgres.Querier).
type store struct {
	q postgres.Querier
}

const userColumns = `id, email, name, role, created_at, password_hash`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &u.passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: scan user: %w", err)
	}
	return u, nil
}

func (s store) createUser(ctx context.Context, email, name string, hash []byte) (User, error) {
	u, err := scanUser(s.q.QueryRow(ctx, `
		INSERT INTO users (email, name, password_hash)
		VALUES ($1, $2, $3)
		RETURNING `+userColumns, email, name, hash))
	if postgres.IsUniqueViolation(err, "users_email_key") {
		return User{}, ErrEmailTaken
	}
	return u, err
}

func (s store) userByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

func (s store) userByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func (s store) setPassword(ctx context.Context, userID string, hash []byte) error {
	_, err := s.q.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, hash)
	if err != nil {
		return fmt.Errorf("auth: set password: %w", err)
	}
	return nil
}

func (s store) setRole(ctx context.Context, email string, role Role) error {
	tag, err := s.q.Exec(ctx, `UPDATE users SET role = $2 WHERE email = $1`, email, role)
	if err != nil {
		return fmt.Errorf("auth: set role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s store) insertRefreshToken(ctx context.Context, userID string, hash []byte, expiresAt time.Time) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hash, expiresAt)
	if err != nil {
		return fmt.Errorf("auth: insert refresh token: %w", err)
	}
	return nil
}

type refreshToken struct {
	id        string
	userID    string
	expiresAt time.Time
	revoked   bool
}

// lockRefreshToken loads a refresh token and locks its row, so two
// concurrent refreshes with the same token cannot both succeed.
func (s store) lockRefreshToken(ctx context.Context, hash []byte) (refreshToken, error) {
	var t refreshToken
	err := s.q.QueryRow(ctx, `
		SELECT id, user_id, expires_at, revoked_at IS NOT NULL
		FROM refresh_tokens WHERE token_hash = $1
		FOR UPDATE`, hash).Scan(&t.id, &t.userID, &t.expiresAt, &t.revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return refreshToken{}, ErrInvalidRefreshToken
	}
	if err != nil {
		return refreshToken{}, fmt.Errorf("auth: load refresh token: %w", err)
	}
	return t, nil
}

func (s store) revokeRefreshToken(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("auth: revoke refresh token: %w", err)
	}
	return nil
}

func (s store) revokeRefreshTokenByHash(ctx context.Context, hash []byte) error {
	_, err := s.q.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, hash)
	if err != nil {
		return fmt.Errorf("auth: revoke refresh token: %w", err)
	}
	return nil
}

// revokeAllRefreshTokens signs the user out of every session.
func (s store) revokeAllRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.q.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil {
		return fmt.Errorf("auth: revoke all refresh tokens: %w", err)
	}
	return nil
}

// replaceResetToken invalidates any outstanding reset token for the user
// and stores a new one, so only the most recent email link works.
func (s store) replaceResetToken(ctx context.Context, userID string, hash []byte, expiresAt time.Time) error {
	if _, err := s.q.Exec(ctx, `
		UPDATE password_reset_tokens SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return fmt.Errorf("auth: invalidate reset tokens: %w", err)
	}
	if _, err := s.q.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hash, expiresAt); err != nil {
		return fmt.Errorf("auth: insert reset token: %w", err)
	}
	return nil
}

// consumeResetToken marks a valid (unused, unexpired) reset token as used
// and returns its user. The single UPDATE makes consumption atomic.
func (s store) consumeResetToken(ctx context.Context, hash []byte, now time.Time) (userID string, err error) {
	err = s.q.QueryRow(ctx, `
		UPDATE password_reset_tokens SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING user_id`, hash, now).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidResetToken
	}
	if err != nil {
		return "", fmt.Errorf("auth: consume reset token: %w", err)
	}
	return userID, nil
}
