package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/FernandaSpineli/techstore/internal/platform/mail"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres/pgtest"
)

type discardMailer struct{}

func (discardMailer) Send(context.Context, mail.Message) error { return nil }

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(Config{
		JWTSecret:       testSecret,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: time.Hour,
		ResetTokenTTL:   30 * time.Minute,
		ResetURL:        "http://shop.test/reset-password",
		BcryptCost:      bcrypt.MinCost,
	}, pgtest.New(t), discardMailer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Wait)
	return svc
}

func TestRefreshTokenExpires(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	_, tokens, err := svc.Register(ctx, "ana@example.com", "Ana", "password123")
	if err != nil {
		t.Fatal(err)
	}

	svc.now = func() time.Time { return time.Now().Add(time.Hour + time.Second) }
	if _, err := svc.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("err = %v, want ErrInvalidRefreshToken", err)
	}
}

// Two requests racing with the same refresh token must not both get a new
// session: the row lock serialises them and the loser is treated as reuse.
func TestConcurrentRefreshSucceedsOnce(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	_, tokens, err := svc.Register(ctx, "ana@example.com", "Ana", "password123")
	if err != nil {
		t.Fatal(err)
	}

	const n = 10
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	for range n {
		wg.Go(func() {
			if _, err := svc.Refresh(ctx, tokens.RefreshToken); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("%d of %d concurrent refreshes succeeded, want exactly 1", successes, n)
	}
}

func TestResetTokenExpires(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	user, _, err := svc.Register(ctx, "ana@example.com", "Ana", "password123")
	if err != nil {
		t.Fatal(err)
	}

	token, hash := newOpaqueToken()
	if err := (store{svc.db}).replaceResetToken(ctx, user.ID, hash, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, token, "new-password-1"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("err = %v, want ErrInvalidResetToken", err)
	}
}

func TestPromoteToAdmin(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	if _, _, err := svc.Register(ctx, "ana@example.com", "Ana", "password123"); err != nil {
		t.Fatal(err)
	}

	if err := svc.PromoteToAdmin(ctx, "ana@example.com"); err != nil {
		t.Fatal(err)
	}
	user, tokens, err := svc.Login(ctx, "ana@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != RoleAdmin {
		t.Errorf("role = %s, want admin", user.Role)
	}
	p, err := svc.Authenticate(tokens.AccessToken)
	if err != nil || p.Role != RoleAdmin {
		t.Errorf("access token principal = %+v (%v), want admin", p, err)
	}

	if err := svc.PromoteToAdmin(ctx, "nobody@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("promote unknown email: err = %v, want ErrUserNotFound", err)
	}
}
