package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/platform/logging"
	"github.com/FernandaSpineli/techstore/internal/platform/mail"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// Config configures the auth Service.
type Config struct {
	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	ResetTokenTTL   time.Duration
	// ResetURL is the page that receives the reset token, e.g.
	// https://shop.example/reset-password. The token is appended as ?token=.
	ResetURL   string
	BcryptCost int
}

// Service implements the authentication use cases.
type Service struct {
	db         *pgxpool.Pool
	tokens     *tokenIssuer
	hasher     *hasher
	mailer     mail.Sender
	refreshTTL time.Duration
	resetTTL   time.Duration
	resetURL   string
	now        func() time.Time

	background sync.WaitGroup
}

// NewService returns a Service.
func NewService(cfg Config, db *pgxpool.Pool, mailer mail.Sender) (*Service, error) {
	h, err := newHasher(cfg.BcryptCost)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:         db,
		tokens:     &tokenIssuer{secret: cfg.JWTSecret, ttl: cfg.AccessTokenTTL, now: time.Now},
		hasher:     h,
		mailer:     mailer,
		refreshTTL: cfg.RefreshTokenTTL,
		resetTTL:   cfg.ResetTokenTTL,
		resetURL:   cfg.ResetURL,
		now:        time.Now,
	}, nil
}

// Wait blocks until background work (outgoing email) has finished. Call it
// during shutdown.
func (s *Service) Wait() { s.background.Wait() }

// Tokens is the credential pair returned after a successful sign-in.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// Register creates a customer account and signs it in.
func (s *Service) Register(ctx context.Context, email, name, password string) (User, Tokens, error) {
	hash, err := s.hasher.hash(password)
	if err != nil {
		return User{}, Tokens{}, err
	}

	var (
		user   User
		tokens Tokens
	)
	err = postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		st := store{tx}
		if user, err = st.createUser(ctx, email, name, hash); err != nil {
			return err
		}
		tokens, err = s.issueTokens(ctx, st, user)
		return err
	})
	if err != nil {
		return User{}, Tokens{}, err
	}

	logging.FromContext(ctx).InfoContext(ctx, "auth.user.registered", "user_id", user.ID)
	return user, tokens, nil
}

// Login verifies credentials and issues tokens. Unknown emails and wrong
// passwords return the same error and take the same time.
func (s *Service) Login(ctx context.Context, email, password string) (User, Tokens, error) {
	log := logging.FromContext(ctx)
	st := store{s.db}

	user, err := st.userByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		return User{}, Tokens{}, err
	}
	if !s.hasher.matches(user.passwordHash, password) {
		// The email is not logged: it is personal data and may be a typo of
		// someone else's address.
		if user.ID != "" {
			log.InfoContext(ctx, "auth.login.failed", "reason", "wrong_password", "user_id", user.ID)
		} else {
			log.InfoContext(ctx, "auth.login.failed", "reason", "unknown_email")
		}
		return User{}, Tokens{}, ErrInvalidCredentials
	}

	tokens, err := s.issueTokens(ctx, st, user)
	if err != nil {
		return User{}, Tokens{}, err
	}
	log.InfoContext(ctx, "auth.login.succeeded", "user_id", user.ID)
	return user, tokens, nil
}

// Refresh rotates a refresh token: the presented token is revoked and a new
// pair is issued. Presenting an already-revoked token means it was copied,
// so every session of that user is revoked (reuse detection).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	var (
		tokens   Tokens
		reuseFor string
	)
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		st := store{tx}
		rt, err := st.lockRefreshToken(ctx, hashToken(refreshToken))
		if err != nil {
			return err
		}
		if rt.revoked {
			reuseFor = rt.userID
			// Commit the mass revocation; the caller still gets an error.
			return st.revokeAllRefreshTokens(ctx, rt.userID)
		}
		if !s.now().Before(rt.expiresAt) {
			return ErrInvalidRefreshToken
		}
		if err := st.revokeRefreshToken(ctx, rt.id); err != nil {
			return err
		}
		user, err := st.userByID(ctx, rt.userID)
		if err != nil {
			return err
		}
		tokens, err = s.issueTokens(ctx, st, user)
		return err
	})
	if err != nil {
		return Tokens{}, err
	}
	if reuseFor != "" {
		logging.FromContext(ctx).WarnContext(ctx, "auth.refresh_token.reuse_detected", "user_id", reuseFor)
		return Tokens{}, ErrInvalidRefreshToken
	}
	return tokens, nil
}

// Logout revokes a refresh token. Unknown tokens are ignored so the endpoint
// reveals nothing.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	return store{s.db}.revokeRefreshTokenByHash(ctx, hashToken(refreshToken))
}

// ChangePassword verifies the current password, sets a new one and signs
// the user out of every other session.
func (s *Service) ChangePassword(ctx context.Context, userID, current, next string) error {
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		st := store{tx}
		user, err := st.userByID(ctx, userID)
		if err != nil {
			return err
		}
		if !s.hasher.matches(user.passwordHash, current) {
			return ErrInvalidCredentials
		}
		return s.setPasswordAndRevoke(ctx, st, userID, next)
	})
	if err != nil {
		return err
	}
	logging.FromContext(ctx).InfoContext(ctx, "auth.password.changed", "user_id", userID)
	return nil
}

// RequestPasswordReset emails a single-use reset link if the account
// exists. It returns nil either way, so the response does not reveal
// whether an email is registered. The email is sent in the background so
// response time does not reveal it either.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	log := logging.FromContext(ctx)
	st := store{s.db}

	user, err := st.userByEmail(ctx, email)
	if errors.Is(err, ErrUserNotFound) {
		log.InfoContext(ctx, "auth.password_reset.requested", "account_exists", false)
		return nil
	}
	if err != nil {
		return err
	}

	token, hash := newOpaqueToken()
	if err := st.replaceResetToken(ctx, user.ID, hash, s.now().Add(s.resetTTL)); err != nil {
		return err
	}
	log.InfoContext(ctx, "auth.password_reset.requested", "account_exists", true, "user_id", user.ID)

	msg := mail.Message{
		To:      user.Email,
		Subject: "Reset your TechStore password",
		Body: fmt.Sprintf("Hi %s,\n\nUse the link below to choose a new password. It expires in %s.\n\n%s?token=%s\n\nIf you did not ask for this, you can ignore this email.\n",
			user.Name, s.resetTTL, s.resetURL, url.QueryEscape(token)),
	}
	// The request context ends with the response; keep its values (request
	// ID, logger) but not its cancellation.
	bgCtx := context.WithoutCancel(ctx)
	s.background.Go(func() {
		ctx, cancel := context.WithTimeout(bgCtx, 30*time.Second)
		defer cancel()
		if err := s.mailer.Send(ctx, msg); err != nil {
			log.ErrorContext(ctx, "mail.send_failed", "template", "password_reset", "user_id", user.ID, "error", err)
			return
		}
		log.InfoContext(ctx, "mail.sent", "template", "password_reset", "user_id", user.ID)
	})
	return nil
}

// ResetPassword sets a new password using a token from a reset email, and
// signs the user out everywhere.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	var userID string
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		st := store{tx}
		var err error
		if userID, err = st.consumeResetToken(ctx, hashToken(token), s.now()); err != nil {
			return err
		}
		return s.setPasswordAndRevoke(ctx, st, userID, password)
	})
	if err != nil {
		return err
	}
	logging.FromContext(ctx).InfoContext(ctx, "auth.password_reset.completed", "user_id", userID)
	return nil
}

// PromoteToAdmin grants the admin role. It is exposed only through the CLI,
// never over HTTP.
func (s *Service) PromoteToAdmin(ctx context.Context, email string) error {
	return store{s.db}.setRole(ctx, email, RoleAdmin)
}

// Authenticate validates an access token.
func (s *Service) Authenticate(token string) (Principal, error) {
	return s.tokens.parse(token)
}

func (s *Service) setPasswordAndRevoke(ctx context.Context, st store, userID, password string) error {
	hash, err := s.hasher.hash(password)
	if err != nil {
		return err
	}
	if err := st.setPassword(ctx, userID, hash); err != nil {
		return err
	}
	return st.revokeAllRefreshTokens(ctx, userID)
}

func (s *Service) issueTokens(ctx context.Context, st store, user User) (Tokens, error) {
	access, err := s.tokens.issue(user.ID, user.Role)
	if err != nil {
		return Tokens{}, err
	}
	refresh, hash := newOpaqueToken()
	if err := st.insertRefreshToken(ctx, user.ID, hash, s.now().Add(s.refreshTTL)); err != nil {
		return Tokens{}, err
	}
	return Tokens{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.tokens.ttl.Seconds()),
		RefreshToken: refresh,
	}, nil
}
