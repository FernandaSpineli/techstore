// Package server wires the API modules into a single http.Handler.
package server

import (
	"log/slog"
	"net/http"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
)

// Deps are the dependencies the HTTP layer needs.
type Deps struct {
	Logger *slog.Logger
}

// NewHandler returns the root handler with every route and the global
// middleware stack.
func NewHandler(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

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
