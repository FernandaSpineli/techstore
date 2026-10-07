package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// Authenticate requires a valid bearer access token and stores the caller's
// Principal in the request context.
func Authenticate(svc *Service) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			token, ok := bearerToken(r)
			if !ok {
				logging.FromContext(ctx).InfoContext(ctx, "auth.unauthorized", "reason", "missing_token")
				unauthorized(w, r, "Authentication required")
				return
			}
			principal, err := svc.Authenticate(token)
			if err != nil {
				logging.FromContext(ctx).InfoContext(ctx, "auth.unauthorized", "reason", "invalid_token", "error", err)
				unauthorized(w, r, "Access token is invalid or expired")
				return
			}

			httpx.AddAccessLogAttrs(ctx, slog.String("user_id", principal.UserID))
			ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("user_id", principal.UserID))
			next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, principal)))
		})
	}
}

// RequireRole allows only callers with role. It must run after Authenticate.
func RequireRole(role Role) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			principal, ok := PrincipalFrom(ctx)
			if !ok || principal.Role != role {
				logging.FromContext(ctx).WarnContext(ctx, "auth.forbidden",
					"required_role", string(role), "role", string(principal.Role), "route", r.Pattern)
				httpx.WriteError(w, r, httpx.NewError(http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this action"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

func unauthorized(w http.ResponseWriter, r *http.Request, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="techstore"`)
	httpx.WriteError(w, r, httpx.NewError(http.StatusUnauthorized, "UNAUTHORIZED", msg))
}
