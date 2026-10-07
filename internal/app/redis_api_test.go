package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginIsRateLimited(t *testing.T) {
	ta := newTestApp(t, withRateLimits(3, 1000))
	login := func() *httptest.ResponseRecorder {
		return ta.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "x@example.com", "password": "guess-1234"}, "")
	}
	for range 3 {
		expectError(t, login(), http.StatusUnauthorized, "INVALID_CREDENTIALS")
	}
	rec := login()
	expectError(t, rec, http.StatusTooManyRequests, "RATE_LIMITED")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	// Browsing the catalog is unaffected by the auth limit.
	expect[productPage](t, ta.do(http.MethodGet, "/api/v1/products", nil, ""), http.StatusOK)
}

func TestHealthProbesAreNeverRateLimited(t *testing.T) {
	ta := newTestApp(t, withRateLimits(1, 1))
	expect[productPage](t, ta.do(http.MethodGet, "/api/v1/products", nil, ""), http.StatusOK)
	expectError(t, ta.do(http.MethodGet, "/api/v1/products", nil, ""), http.StatusTooManyRequests, "RATE_LIMITED")
	for range 5 {
		expect[map[string]string](t, ta.do(http.MethodGet, "/healthz", nil, ""), http.StatusOK)
	}
}

func TestCatalogIsCachedAndInvalidatedByAdminWrites(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()

	first := ta.do(http.MethodGet, "/api/v1/products/iphone-15", nil, "")
	second := ta.do(http.MethodGet, "/api/v1/products/iphone-15", nil, "")
	if first.Header().Get("X-Cache") != "MISS" || second.Header().Get("X-Cache") != "HIT" || first.Body.String() != second.Body.String() {
		t.Fatalf("cache headers = %q then %q", first.Header().Get("X-Cache"), second.Header().Get("X-Cache"))
	}

	expect[product](t, ta.do(http.MethodPatch, "/api/v1/products/"+c.iphone.ID, map[string]string{"name": "iPhone 15 (2023)"}, c.admin), http.StatusOK)
	after := ta.do(http.MethodGet, "/api/v1/products/iphone-15", nil, "")
	if after.Header().Get("X-Cache") != "MISS" || expect[product](t, after, http.StatusOK).Name != "iPhone 15 (2023)" {
		t.Errorf("stale product served after an admin update: %s", after.Body)
	}

	// A failed admin write does not invalidate.
	ta.do(http.MethodGet, "/api/v1/products/iphone-15", nil, "")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/products/"+c.iphone.ID, map[string]string{"status": "bogus"}, c.admin),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	if rec := ta.do(http.MethodGet, "/api/v1/products/iphone-15", nil, ""); rec.Header().Get("X-Cache") != "HIT" {
		t.Error("a rejected write invalidated the cache")
	}

	// Errors are not cached: a product created after a 404 is found.
	expectError(t, ta.do(http.MethodGet, "/api/v1/products/new-thing", nil, ""), http.StatusNotFound, "PRODUCT_NOT_FOUND")
	p := ta.createProduct(c.admin, map[string]any{"name": "New Thing", "brand": "Acme", "category_id": c.phones.ID, "status": "active"})
	ta.createVariant(c.admin, p.ID, "NEW-1", 1000, 1)
	expect[product](t, ta.do(http.MethodGet, "/api/v1/products/new-thing", nil, ""), http.StatusOK)
}

func TestOrderCreationIsIdempotent(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")
	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 1), http.StatusOK)

	post := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
		req.Header.Set("Authorization", "Bearer "+user)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		ta.h.ServeHTTP(rec, req)
		return rec
	}

	first := post("order-attempt-1")
	retry := post("order-attempt-1") // e.g. the response was lost and the client retried
	a, b := expect[orderView](t, first, http.StatusCreated), expect[orderView](t, retry, http.StatusCreated)
	if a.ID != b.ID || retry.Header().Get("Idempotent-Replayed") != "true" || retry.Header().Get("Location") != first.Header().Get("Location") {
		t.Errorf("retry = %s (%v), want a replay of %s", b.ID, retry.Header(), a.ID)
	}

	var orders int
	if err := ta.db.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if orders != 1 {
		t.Errorf("%d orders created, want 1", orders)
	}

	// A new key is a new request: the cart is now empty.
	expectError(t, post("order-attempt-2"), http.StatusUnprocessableEntity, "CART_EMPTY")
}
