package app

import (
	"net/http"
	"strings"
	"time"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/cart"
	"github.com/FernandaSpineli/techstore/internal/catalog"
	"github.com/FernandaSpineli/techstore/internal/inventory"
	"github.com/FernandaSpineli/techstore/internal/order"
	"github.com/FernandaSpineli/techstore/internal/payment"
	"github.com/FernandaSpineli/techstore/internal/platform/cache"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/idempotency"
	"github.com/FernandaSpineli/techstore/internal/platform/ratelimit"
	"github.com/FernandaSpineli/techstore/internal/user"
	"github.com/FernandaSpineli/techstore/web"
)

// handlers are the HTTP entry points of every module.
type handlers struct {
	auth      *auth.Handler
	user      *user.Handler
	catalog   *catalog.Handler
	inventory *inventory.Handler
	cart      *cart.Handler
	order     *order.Handler
	payment   *payment.Handler
}

// routes lists every endpoint in one place, with the middleware that guards
// it, so the API surface and its access rules can be reviewed at a glance.
func routes(d Deps, authSvc *auth.Service, hs handlers) http.Handler {
	mux := http.NewServeMux()
	signedIn := auth.Authenticate(authSvc)
	admin := func(next http.Handler) http.Handler {
		return signedIn(auth.RequireRole(auth.RoleAdmin)(next))
	}
	authH, userH, catalogH, inventoryH, cartH, orderH, paymentH :=
		hs.auth, hs.user, hs.catalog, hs.inventory, hs.cart, hs.order, hs.payment
	rc := newRedisFeatures(d)

	type h = httpx.HandlerFunc

	// Demo storefront (same origin as the API)
	for _, page := range []string{"GET /{$}", "GET /reset-password", "GET /checkout/success", "GET /checkout/cancel"} {
		mux.HandleFunc(page, web.Page)
	}
	mux.Handle("GET /assets/", web.Assets())

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", readyz(d.ReadinessChecks))

	// Auth (public, with a strict per-IP limit against credential stuffing)
	mux.Handle("POST /api/v1/auth/register", rc.authLimit(h(authH.Register)))
	mux.Handle("POST /api/v1/auth/login", rc.authLimit(h(authH.Login)))
	mux.Handle("POST /api/v1/auth/refresh", rc.authLimit(h(authH.Refresh)))
	mux.Handle("POST /api/v1/auth/logout", h(authH.Logout))
	mux.Handle("POST /api/v1/auth/password/forgot", rc.authLimit(h(authH.ForgotPassword)))
	mux.Handle("POST /api/v1/auth/password/reset", rc.authLimit(h(authH.ResetPassword)))

	// Signed-in user
	mux.Handle("GET /api/v1/users/me", signedIn(h(userH.Me)))
	mux.Handle("PATCH /api/v1/users/me", signedIn(h(userH.UpdateMe)))
	mux.Handle("PUT /api/v1/users/me/password", signedIn(h(authH.ChangePassword)))

	// Catalog (public, cached)
	mux.Handle("GET /api/v1/categories", rc.cached(h(catalogH.ListCategories)))
	mux.Handle("GET /api/v1/products", rc.cached(h(catalogH.ListProducts)))
	mux.Handle("GET /api/v1/products/{slug}", rc.cached(h(catalogH.GetProduct)))

	// Cart (signed-in user)
	mux.Handle("GET /api/v1/cart", signedIn(h(cartH.Get)))
	mux.Handle("DELETE /api/v1/cart", signedIn(h(cartH.Clear)))
	mux.Handle("POST /api/v1/cart/items", signedIn(h(cartH.AddItem)))
	mux.Handle("PATCH /api/v1/cart/items/{variantId}", signedIn(h(cartH.UpdateItem)))
	mux.Handle("DELETE /api/v1/cart/items/{variantId}", signedIn(h(cartH.RemoveItem)))

	// Orders (signed-in user)
	mux.Handle("POST /api/v1/orders", signedIn(rc.idempotent(h(orderH.Create))))
	mux.Handle("GET /api/v1/orders", signedIn(h(orderH.List)))
	mux.Handle("GET /api/v1/orders/{id}", signedIn(h(orderH.Get)))
	mux.Handle("POST /api/v1/orders/{id}/cancel", signedIn(h(orderH.Cancel)))
	mux.Handle("POST /api/v1/orders/{id}/checkout", signedIn(h(paymentH.Checkout)))

	// Payments (public: authenticated by the Stripe signature)
	mux.Handle("POST /api/v1/payments/webhook", h(paymentH.Webhook))

	// Orders (admin)
	mux.Handle("GET /api/v1/admin/orders", admin(h(orderH.AdminList)))
	mux.Handle("GET /api/v1/admin/orders/{id}", admin(h(orderH.AdminGet)))
	mux.Handle("PATCH /api/v1/admin/orders/{id}/status", admin(h(orderH.AdminSetStatus)))

	// Catalog and inventory (admin). Successful writes invalidate the cache.
	mux.Handle("POST /api/v1/categories", admin(rc.invalidates(h(catalogH.CreateCategory))))
	mux.Handle("PATCH /api/v1/categories/{id}", admin(rc.invalidates(h(catalogH.UpdateCategory))))
	mux.Handle("DELETE /api/v1/categories/{id}", admin(rc.invalidates(h(catalogH.DeleteCategory))))
	mux.Handle("GET /api/v1/admin/products", admin(h(catalogH.AdminListProducts)))
	mux.Handle("GET /api/v1/admin/products/{id}", admin(h(catalogH.AdminGetProduct)))
	mux.Handle("POST /api/v1/products", admin(rc.invalidates(h(catalogH.CreateProduct))))
	mux.Handle("PATCH /api/v1/admin/products/{id}", admin(rc.invalidates(h(catalogH.UpdateProduct))))
	mux.Handle("DELETE /api/v1/admin/products/{id}", admin(rc.invalidates(h(catalogH.DeleteProduct))))
	mux.Handle("POST /api/v1/products/{id}/variants", admin(rc.invalidates(h(catalogH.CreateVariant))))
	mux.Handle("PATCH /api/v1/variants/{id}", admin(rc.invalidates(h(catalogH.UpdateVariant))))
	mux.Handle("GET /api/v1/variants/{id}/inventory", admin(h(inventoryH.Get)))
	mux.Handle("PUT /api/v1/variants/{id}/inventory", admin(rc.invalidates(h(inventoryH.Set))))

	// Unknown routes get the standard error envelope instead of net/http's
	// plain-text 404.
	mux.Handle("/", h(func(http.ResponseWriter, *http.Request) error {
		return httpx.NewError(http.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found")
	}))

	return httpx.Chain(mux,
		httpx.RequestID,
		httpx.AccessLog(d.Logger),
		httpx.Recover,
		httpx.SecurityHeaders,
		httpx.CORS(d.Config.CORSAllowedOrigins),
		rc.apiLimit,
	)
}

// redisFeatures holds the Redis-backed middleware. Without Redis each one
// is a pass-through.
type redisFeatures struct {
	authLimit, apiLimit, cached, invalidates, idempotent httpx.Middleware
}

func newRedisFeatures(d Deps) redisFeatures {
	pass := func(next http.Handler) http.Handler { return next }
	if d.Redis == nil {
		return redisFeatures{pass, pass, pass, pass, pass}
	}

	api := ratelimit.New(d.Redis, "api", d.Config.APIRateLimit, time.Minute)
	catalogCache := cache.New(d.Redis, "catalog", d.Config.CatalogCacheTTL)
	idem := idempotency.New(d.Redis, 24*time.Hour)

	return redisFeatures{
		authLimit: ratelimit.New(d.Redis, "auth", d.Config.AuthRateLimit, time.Minute).Middleware,
		// Health probes are exempt: an orchestrator polling them must never
		// be throttled.
		apiLimit: func(next http.Handler) http.Handler {
			limited := api.Middleware(next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					limited.ServeHTTP(w, r)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		cached:      catalogCache.Middleware,
		invalidates: catalogCache.InvalidateOnWrite,
		idempotent: idem.Middleware(func(r *http.Request) string {
			p, _ := auth.PrincipalFrom(r.Context())
			return p.UserID
		}),
	}
}
