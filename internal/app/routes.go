package app

import (
	"net/http"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/user"
)

// routes lists every endpoint in one place, with the middleware that guards
// it, so the API surface and its access rules can be reviewed at a glance.
func routes(d Deps, authSvc *auth.Service, authH *auth.Handler, userH *user.Handler) http.Handler {
	mux := http.NewServeMux()
	signedIn := auth.Authenticate(authSvc)

	type h = httpx.HandlerFunc

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", readyz(d.ReadinessChecks))

	// Auth (public)
	mux.Handle("POST /api/v1/auth/register", h(authH.Register))
	mux.Handle("POST /api/v1/auth/login", h(authH.Login))
	mux.Handle("POST /api/v1/auth/refresh", h(authH.Refresh))
	mux.Handle("POST /api/v1/auth/logout", h(authH.Logout))
	mux.Handle("POST /api/v1/auth/password/forgot", h(authH.ForgotPassword))
	mux.Handle("POST /api/v1/auth/password/reset", h(authH.ResetPassword))

	// Signed-in user
	mux.Handle("GET /api/v1/users/me", signedIn(h(userH.Me)))
	mux.Handle("PATCH /api/v1/users/me", signedIn(h(userH.UpdateMe)))
	mux.Handle("PUT /api/v1/users/me/password", signedIn(h(authH.ChangePassword)))

	// Unknown routes get the standard error envelope instead of net/http's
	// plain-text 404.
	mux.Handle("/", h(func(http.ResponseWriter, *http.Request) error {
		return httpx.NewError(http.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found")
	}))

	return httpx.Chain(mux,
		httpx.RequestID,
		httpx.AccessLog(d.Logger),
		httpx.Recover,
	)
}
