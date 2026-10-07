package app

import (
	"net/http"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/cart"
	"github.com/FernandaSpineli/techstore/internal/catalog"
	"github.com/FernandaSpineli/techstore/internal/inventory"
	"github.com/FernandaSpineli/techstore/internal/order"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/user"
)

// handlers are the HTTP entry points of every module.
type handlers struct {
	auth      *auth.Handler
	user      *user.Handler
	catalog   *catalog.Handler
	inventory *inventory.Handler
	cart      *cart.Handler
	order     *order.Handler
}

// routes lists every endpoint in one place, with the middleware that guards
// it, so the API surface and its access rules can be reviewed at a glance.
func routes(d Deps, authSvc *auth.Service, hs handlers) http.Handler {
	mux := http.NewServeMux()
	signedIn := auth.Authenticate(authSvc)
	admin := func(next http.Handler) http.Handler {
		return signedIn(auth.RequireRole(auth.RoleAdmin)(next))
	}
	authH, userH, catalogH, inventoryH, cartH, orderH := hs.auth, hs.user, hs.catalog, hs.inventory, hs.cart, hs.order

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

	// Catalog (public)
	mux.Handle("GET /api/v1/categories", h(catalogH.ListCategories))
	mux.Handle("GET /api/v1/products", h(catalogH.ListProducts))
	mux.Handle("GET /api/v1/products/{slug}", h(catalogH.GetProduct))

	// Cart (signed-in user)
	mux.Handle("GET /api/v1/cart", signedIn(h(cartH.Get)))
	mux.Handle("DELETE /api/v1/cart", signedIn(h(cartH.Clear)))
	mux.Handle("POST /api/v1/cart/items", signedIn(h(cartH.AddItem)))
	mux.Handle("PATCH /api/v1/cart/items/{variantId}", signedIn(h(cartH.UpdateItem)))
	mux.Handle("DELETE /api/v1/cart/items/{variantId}", signedIn(h(cartH.RemoveItem)))

	// Orders (signed-in user)
	mux.Handle("POST /api/v1/orders", signedIn(h(orderH.Create)))
	mux.Handle("GET /api/v1/orders", signedIn(h(orderH.List)))
	mux.Handle("GET /api/v1/orders/{id}", signedIn(h(orderH.Get)))
	mux.Handle("POST /api/v1/orders/{id}/cancel", signedIn(h(orderH.Cancel)))

	// Orders (admin)
	mux.Handle("GET /api/v1/admin/orders", admin(h(orderH.AdminList)))
	mux.Handle("GET /api/v1/admin/orders/{id}", admin(h(orderH.AdminGet)))
	mux.Handle("PATCH /api/v1/admin/orders/{id}/status", admin(h(orderH.AdminSetStatus)))

	// Catalog and inventory (admin)
	mux.Handle("POST /api/v1/categories", admin(h(catalogH.CreateCategory)))
	mux.Handle("PATCH /api/v1/categories/{id}", admin(h(catalogH.UpdateCategory)))
	mux.Handle("DELETE /api/v1/categories/{id}", admin(h(catalogH.DeleteCategory)))
	mux.Handle("GET /api/v1/admin/products", admin(h(catalogH.AdminListProducts)))
	mux.Handle("GET /api/v1/admin/products/{id}", admin(h(catalogH.AdminGetProduct)))
	mux.Handle("POST /api/v1/products", admin(h(catalogH.CreateProduct)))
	mux.Handle("PATCH /api/v1/products/{id}", admin(h(catalogH.UpdateProduct)))
	mux.Handle("DELETE /api/v1/products/{id}", admin(h(catalogH.DeleteProduct)))
	mux.Handle("POST /api/v1/products/{id}/variants", admin(h(catalogH.CreateVariant)))
	mux.Handle("PATCH /api/v1/variants/{id}", admin(h(catalogH.UpdateVariant)))
	mux.Handle("GET /api/v1/variants/{id}/inventory", admin(h(inventoryH.Get)))
	mux.Handle("PUT /api/v1/variants/{id}/inventory", admin(h(inventoryH.Set)))

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
