package app_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type cartItem struct {
	VariantID         string `json:"variant_id"`
	SKU               string `json:"sku"`
	ProductSlug       string `json:"product_slug"`
	UnitPriceCents    int64  `json:"unit_price_cents"`
	Quantity          int    `json:"quantity"`
	LineTotalCents    int64  `json:"line_total_cents"`
	Issue             string `json:"issue"`
	AvailableQuantity *int   `json:"available_quantity"`
}

type cartView struct {
	Items         []cartItem `json:"items"`
	ItemCount     int        `json:"item_count"`
	SubtotalCents int64      `json:"subtotal_cents"`
	Currency      string     `json:"currency"`
	CheckoutReady bool       `json:"checkout_ready"`
}

func (ta *testApp) addToCart(token, variantID string, qty int) *httptest.ResponseRecorder {
	return ta.do(http.MethodPost, "/api/v1/cart/items", map[string]any{"variant_id": variantID, "quantity": qty}, token)
}

func TestCartRequiresAuthentication(t *testing.T) {
	ta := newTestApp(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/cart"},
		{http.MethodDelete, "/api/v1/cart"},
		{http.MethodPost, "/api/v1/cart/items"},
		{http.MethodPatch, "/api/v1/cart/items/01a11699-7a06-7d19-ace9-45b3da7b647b"},
		{http.MethodDelete, "/api/v1/cart/items/01a11699-7a06-7d19-ace9-45b3da7b647b"},
	} {
		expectError(t, ta.do(r.method, r.path, nil, ""), http.StatusUnauthorized, "UNAUTHORIZED")
	}
}

func TestCartLifecycle(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")

	empty := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, user), http.StatusOK)
	if empty.Items == nil || len(empty.Items) != 0 || empty.CheckoutReady || empty.Currency != "brl" {
		t.Fatalf("empty cart = %+v", empty)
	}

	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 1), http.StatusOK)
	got := expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 2), http.StatusOK) // same variant: increments
	if len(got.Items) != 1 || got.Items[0].Quantity != 3 || got.SubtotalCents != 3*499900 || !got.CheckoutReady {
		t.Fatalf("cart after adds = %+v", got)
	}

	galaxy := expect[product](t, ta.do(http.MethodGet, "/api/v1/products/galaxy-s24", nil, ""), http.StatusOK).Variants[0]
	got = expect[cartView](t, ta.addToCart(user, galaxy.ID, 1), http.StatusOK)
	if got.ItemCount != 4 || got.SubtotalCents != 3*499900+399900 {
		t.Fatalf("cart totals = %d items / %d cents", got.ItemCount, got.SubtotalCents)
	}

	got = expect[cartView](t, ta.do(http.MethodPatch, "/api/v1/cart/items/"+c.iphone128.ID, map[string]int{"quantity": 1}, user), http.StatusOK)
	if got.Items[0].Quantity != 1 || got.Items[0].LineTotalCents != 499900 {
		t.Fatalf("after update = %+v", got.Items[0])
	}

	got = expect[cartView](t, ta.do(http.MethodDelete, "/api/v1/cart/items/"+galaxy.ID, nil, user), http.StatusOK)
	if len(got.Items) != 1 {
		t.Fatalf("after remove = %+v", got)
	}
	expectError(t, ta.do(http.MethodDelete, "/api/v1/cart/items/"+galaxy.ID, nil, user), http.StatusNotFound, "CART_ITEM_NOT_FOUND")

	expect[any](t, ta.do(http.MethodDelete, "/api/v1/cart", nil, user), http.StatusNoContent)
	if got := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, user), http.StatusOK); len(got.Items) != 0 {
		t.Fatalf("cart after clear = %+v", got)
	}
}

func TestCartIsPrivateToEachUser(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	ana, bia := ta.signIn("customer"), ta.signIn("customer")

	expect[cartView](t, ta.addToCart(ana, c.iphone128.ID, 1), http.StatusOK)

	if got := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, bia), http.StatusOK); len(got.Items) != 0 {
		t.Fatalf("bia sees ana's cart: %+v", got)
	}
	expectError(t, ta.do(http.MethodDelete, "/api/v1/cart/items/"+c.iphone128.ID, nil, bia), http.StatusNotFound, "CART_ITEM_NOT_FOUND")
	if got := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, ana), http.StatusOK); len(got.Items) != 1 {
		t.Fatalf("ana's cart was changed by bia: %+v", got)
	}
}

func TestCartValidatesStock(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog() // IP15-128 has 5 units, IP15-256 has none
	user := ta.signIn("customer")

	e := expectError(t, ta.addToCart(user, c.iphone128.ID, 6), http.StatusConflict, "INSUFFICIENT_STOCK")
	var details map[string]int
	if err := json.Unmarshal(e.Error.Details, &details); err != nil || details["available"] != 5 || details["requested"] != 6 {
		t.Errorf("details = %s, want available 5 and requested 6", e.Error.Details)
	}

	// The check covers what is already in the cart.
	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 4), http.StatusOK)
	expectError(t, ta.addToCart(user, c.iphone128.ID, 2), http.StatusConflict, "INSUFFICIENT_STOCK")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/cart/items/"+c.iphone128.ID, map[string]int{"quantity": 6}, user),
		http.StatusConflict, "INSUFFICIENT_STOCK")

	expectError(t, ta.addToCart(user, c.i256.ID, 1), http.StatusConflict, "INSUFFICIENT_STOCK")
}

func TestCartRejectsUnsellableAndUnknownVariants(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")

	pixel := expect[product](t, ta.do(http.MethodGet, "/api/v1/admin/products/"+c.pixel.ID, nil, c.admin), http.StatusOK).Variants[0]
	expectError(t, ta.addToCart(user, pixel.ID, 1), http.StatusConflict, "VARIANT_UNAVAILABLE") // draft product

	expect[variant](t, ta.do(http.MethodPatch, "/api/v1/variants/"+c.iphone128.ID, map[string]bool{"active": false}, c.admin), http.StatusOK)
	expectError(t, ta.addToCart(user, c.iphone128.ID, 1), http.StatusConflict, "VARIANT_UNAVAILABLE")

	expectError(t, ta.addToCart(user, "01a11699-7a06-7d19-ace9-45b3da7b647b", 1), http.StatusNotFound, "VARIANT_NOT_FOUND")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/cart/items/"+c.galaxy.ID, map[string]int{"quantity": 1}, user),
		http.StatusNotFound, "VARIANT_NOT_FOUND") // a product ID is not a variant ID
}

func TestCartValidatesInput(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")

	for name, body := range map[string]any{
		"zero quantity":     map[string]any{"variant_id": c.iphone128.ID, "quantity": 0},
		"negative quantity": map[string]any{"variant_id": c.iphone128.ID, "quantity": -2},
		"over the cap":      map[string]any{"variant_id": c.iphone128.ID, "quantity": 100},
		"missing quantity":  map[string]any{"variant_id": c.iphone128.ID},
		"malformed variant": map[string]any{"variant_id": "abc", "quantity": 1},
	} {
		t.Run(name, func(t *testing.T) {
			expectError(t, ta.do(http.MethodPost, "/api/v1/cart/items", body, user), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
		})
	}
	expectError(t, ta.do(http.MethodPost, "/api/v1/cart/items", map[string]any{"variant_id": c.iphone128.ID, "quantity": "2"}, user),
		http.StatusBadRequest, "INVALID_JSON")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/cart/items/"+c.iphone128.ID, map[string]int{"quantity": 1}, user),
		http.StatusNotFound, "CART_ITEM_NOT_FOUND")
}

func TestCartFlagsLinesThatBecameUnavailable(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")
	galaxy := expect[product](t, ta.do(http.MethodGet, "/api/v1/products/galaxy-s24", nil, ""), http.StatusOK).Variants[0]

	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 4), http.StatusOK)
	expect[cartView](t, ta.addToCart(user, galaxy.ID, 1), http.StatusOK)

	// Stock drops, a product is archived and a price changes after the fact.
	expect[stockLevel](t, ta.do(http.MethodPut, "/api/v1/variants/"+c.iphone128.ID+"/inventory", map[string]int{"on_hand": 2}, c.admin), http.StatusOK)
	expect[product](t, ta.do(http.MethodPatch, "/api/v1/admin/products/"+c.galaxy.ID, map[string]string{"status": "archived"}, c.admin), http.StatusOK)
	expect[variant](t, ta.do(http.MethodPatch, "/api/v1/variants/"+c.iphone128.ID, map[string]int{"price_cents": 459900}, c.admin), http.StatusOK)

	got := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, user), http.StatusOK)
	if got.CheckoutReady {
		t.Error("cart with problems is checkout ready")
	}
	iphone, gal := got.Items[0], got.Items[1]
	if iphone.Issue != "insufficient_stock" || iphone.AvailableQuantity == nil || *iphone.AvailableQuantity != 2 {
		t.Errorf("iphone line = %+v, want insufficient_stock with 2 available", iphone)
	}
	if iphone.UnitPriceCents != 459900 {
		t.Errorf("cart shows price %d, want the current price 459900", iphone.UnitPriceCents)
	}
	if gal.Issue != "unavailable" {
		t.Errorf("galaxy line issue = %q, want unavailable", gal.Issue)
	}

	// Fixing the lines makes the cart ready again.
	expect[cartView](t, ta.do(http.MethodPatch, "/api/v1/cart/items/"+c.iphone128.ID, map[string]int{"quantity": 2}, user), http.StatusOK)
	got = expect[cartView](t, ta.do(http.MethodDelete, "/api/v1/cart/items/"+galaxy.ID, nil, user), http.StatusOK)
	if !got.CheckoutReady {
		t.Errorf("cart = %+v, want checkout ready", got)
	}
}

// Concurrent adds from one user are serialised by the cart row lock: every
// successful add must count, and none may push the cart past the stock.
func TestConcurrentAddsNeverExceedStock(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog() // 5 units
	user := ta.signIn("customer")

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	for range 20 {
		wg.Go(func() {
			if ta.addToCart(user, c.iphone128.ID, 1).Code == http.StatusOK {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	got := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, user), http.StatusOK)
	if len(got.Items) != 1 || got.Items[0].Quantity != 5 || successes != 5 {
		t.Fatalf("%d adds succeeded and the cart holds %+v; want 5 and 5 (lost or excess updates)", successes, got.Items)
	}
}
