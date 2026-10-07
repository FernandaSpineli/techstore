package app

import (
	"context"
	"net/http"
	"time"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

func healthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
