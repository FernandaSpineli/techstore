// Package server wires the API modules into a single http.Handler.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// Deps are the dependencies the HTTP layer needs.
type Deps struct {
	Logger *slog.Logger
	// ReadinessChecks are run by GET /readyz, keyed by dependency name.
	ReadinessChecks map[string]func(context.Context) error
}

// NewHandler returns the root handler with every route and the global
// middleware stack.
func NewHandler(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", readyz(deps.ReadinessChecks))

	// Unknown routes get the standard error envelope instead of net/http's
	// plain-text 404.
	mux.Handle("/", httpx.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		return httpx.NewError(http.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found")
	}))

	return httpx.Chain(mux,
		httpx.RequestID,
		httpx.AccessLog(deps.Logger),
		httpx.Recover,
	)
}

// readyz reports whether every dependency is reachable. Failures are logged
// but only "unavailable" is returned, so the endpoint does not reveal
// infrastructure details.
func readyz(checks map[string]func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status, results := http.StatusOK, make(map[string]string, len(checks))
		for name, check := range checks {
			if err := check(ctx); err != nil {
				logging.FromContext(ctx).WarnContext(ctx, "readiness.check_failed", "dependency", name, "error", err)
				results[name] = "unavailable"
				status = http.StatusServiceUnavailable
				continue
			}
			results[name] = "ok"
		}

		overall := "ok"
		if status != http.StatusOK {
			overall = "unavailable"
		}
		httpx.WriteJSON(w, status, map[string]any{"status": overall, "checks": results})
	}
}
