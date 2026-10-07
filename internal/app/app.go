// Package app assembles the API: it builds every module from its
// dependencies and wires them to routes. cmd/api and the API tests both go
// through New, so tests exercise the real wiring.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/catalog"
	"github.com/FernandaSpineli/techstore/internal/inventory"
	"github.com/FernandaSpineli/techstore/internal/platform/config"
	"github.com/FernandaSpineli/techstore/internal/platform/mail"
	"github.com/FernandaSpineli/techstore/internal/user"
)

// Deps are the external resources the application runs on.
type Deps struct {
	Config config.Config
	Logger *slog.Logger
	DB     *pgxpool.Pool
	Mailer mail.Sender
	// ReadinessChecks are run by GET /readyz, keyed by dependency name.
	ReadinessChecks map[string]func(context.Context) error
	// BcryptCost overrides auth.DefaultBcryptCost; tests lower it for speed.
	BcryptCost int
}

// App is the assembled application.
type App struct {
	Handler http.Handler
	auth    *auth.Service
}

// New builds the application.
func New(d Deps) (*App, error) {
	cost := d.BcryptCost
	if cost == 0 {
		cost = auth.DefaultBcryptCost
	}
	authSvc, err := auth.NewService(auth.Config{
		JWTSecret:       []byte(d.Config.JWTSecret.Reveal()),
		AccessTokenTTL:  d.Config.AccessTokenTTL,
		RefreshTokenTTL: d.Config.RefreshTokenTTL,
		ResetTokenTTL:   d.Config.PasswordResetTTL,
		ResetURL:        strings.TrimRight(d.Config.BaseURL, "/") + "/reset-password",
		BcryptCost:      cost,
	}, d.DB, d.Mailer)
	if err != nil {
		return nil, err
	}

	a := &App{auth: authSvc}
	a.Handler = routes(d, authSvc, handlers{
		auth:      auth.NewHandler(authSvc),
		user:      user.NewHandler(d.DB),
		catalog:   catalog.NewHandler(catalog.NewStore(d.DB)),
		inventory: inventory.NewHandler(inventory.NewStore(d.DB)),
	})
	return a, nil
}

// Wait blocks until background work started by requests (such as outgoing
// email) has finished. Call it after the HTTP server has shut down.
func (a *App) Wait() { a.auth.Wait() }
