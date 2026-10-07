package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

type orderItem struct {
	VariantID      string `json:"variant_id"`
	ProductName    string `json:"product_name"`
	SKU            string `json:"sku"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	Quantity       int    `json:"quantity"`
	LineTotalCents int64  `json:"line_total_cents"`
}

type orderView struct {
	ID            string      `json:"id"`
	Status        string      `json:"status"`
	Currency      string      `json:"currency"`
	TotalCents    int64       `json:"total_cents"`
	ExpiresAt     *time.Time  `json:"expires_at"`
	Items         []orderItem `json:"items"`
	StatusHistory []struct {
		From   *string `json:"from"`
		To     string  `json:"to"`
		Reason string  `json:"reason"`
	} `json:"status_history"`
}

type orderPage struct {
	Data []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ItemCount int    `json:"item_count"`
	} `json:"data"`
	Pagination struct {
		Total int `json:"total"`
	} `json:"pagination"`
}

func (ta *testApp) placeOrder(token string) orderView {
	ta.t.Helper()
	return expect[orderView](ta.t, ta.do(http.MethodPost, "/api/v1/orders", nil, token), http.StatusCreated)
}

func (ta *testApp) stock(admin, variantID string) stockLevel {
	ta.t.Helper()
	return expect[stockLevel](ta.t, ta.do(http.MethodGet, "/api/v1/variants/"+variantID+"/inventory", nil, admin), http.StatusOK)
}

func TestOrderRoutesRequireAuthentication(t *testing.T) {
	ta := newTestApp(t)
	customer := ta.signIn("customer")
	id := "01a11699-7a06-7d19-ace9-45b3da7b647b"
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/orders"},
		{http.MethodGet, "/api/v1/orders"},
		{http.MethodGet, "/api/v1/orders/" + id},
		{http.MethodPost, "/api/v1/orders/" + id + "/cancel"},
	} {
		expectError(t, ta.do(r.method, r.path, nil, ""), http.StatusUnauthorized, "UNAUTHORIZED")
	}
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/orders"},
		{http.MethodGet, "/api/v1/admin/orders/" + id},
		{http.MethodPatch, "/api/v1/admin/orders/" + id + "/status"},
	} {
		expectError(t, ta.do(r.method, r.path, nil, customer), http.StatusForbidden, "FORBIDDEN")
	}
}

func TestPlaceOrder(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")
	galaxy := expect[product](t, ta.do(http.MethodGet, "/api/v1/products/galaxy-s24", nil, ""), http.StatusOK).Variants[0]

	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 2), http.StatusOK)
	expect[cartView](t, ta.addToCart(user, galaxy.ID, 1), http.StatusOK)

	rec := ta.do(http.MethodPost, "/api/v1/orders", nil, user)
	o := expect[orderView](t, rec, http.StatusCreated)
	if rec.Header().Get("Location") != "/api/v1/orders/"+o.ID {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	if o.Status != "pending_payment" || o.Currency != "brl" || o.TotalCents != 2*499900+399900 || o.ExpiresAt == nil {
		t.Errorf("order = %+v", o)
	}
	if len(o.Items) != 2 || len(o.StatusHistory) != 1 || o.StatusHistory[0].From != nil || o.StatusHistory[0].To != "pending_payment" {
		t.Errorf("items/history = %+v / %+v", o.Items, o.StatusHistory)
	}

	if got := ta.stock(c.admin, c.iphone128.ID); got != (stockLevel{OnHand: 5, Reserved: 2, Available: 3}) {
		t.Errorf("iphone stock = %+v, want 2 reserved", got)
	}
	if cart := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, user), http.StatusOK); len(cart.Items) != 0 {
		t.Errorf("cart not emptied: %+v", cart)
	}

	// Later catalog changes never alter a placed order.
	expect[variant](t, ta.do(http.MethodPatch, "/api/v1/variants/"+c.iphone128.ID, map[string]int{"price_cents": 1}, c.admin), http.StatusOK)
	again := expect[orderView](t, ta.do(http.MethodGet, "/api/v1/orders/"+o.ID, nil, user), http.StatusOK)
	if again.TotalCents != o.TotalCents || again.Items[1].UnitPriceCents != 499900 {
		t.Errorf("order changed after price update: %+v", again)
	}
}

func TestPlaceOrderFailuresChangeNothing(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")

	expectError(t, ta.do(http.MethodPost, "/api/v1/orders", nil, user), http.StatusUnprocessableEntity, "CART_EMPTY")

	// Stock drops below what is in the cart.
	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 4), http.StatusOK)
	expect[stockLevel](t, ta.do(http.MethodPut, "/api/v1/variants/"+c.iphone128.ID+"/inventory", map[string]int{"on_hand": 3}, c.admin), http.StatusOK)
	e := expectError(t, ta.do(http.MethodPost, "/api/v1/orders", nil, user), http.StatusConflict, "INSUFFICIENT_STOCK")
	var shortages []struct {
		VariantID string `json:"variant_id"`
		Requested int    `json:"requested"`
		Available int    `json:"available"`
	}
	if err := json.Unmarshal(e.Error.Details, &shortages); err != nil || len(shortages) != 1 ||
		shortages[0].VariantID != c.iphone128.ID || shortages[0].Requested != 4 || shortages[0].Available != 3 {
		t.Errorf("shortages = %s", e.Error.Details)
	}

	// A product in the cart is archived.
	expect[cartView](t, ta.do(http.MethodPatch, "/api/v1/cart/items/"+c.iphone128.ID, map[string]int{"quantity": 1}, user), http.StatusOK)
	expect[product](t, ta.do(http.MethodPatch, "/api/v1/products/"+c.iphone.ID, map[string]string{"status": "archived"}, c.admin), http.StatusOK)
	expectError(t, ta.do(http.MethodPost, "/api/v1/orders", nil, user), http.StatusConflict, "CART_HAS_UNAVAILABLE_ITEMS")

	if got := ta.stock(c.admin, c.iphone128.ID); got.Reserved != 0 {
		t.Errorf("failed orders reserved stock: %+v", got)
	}
	if cart := expect[cartView](t, ta.do(http.MethodGet, "/api/v1/cart", nil, user), http.StatusOK); len(cart.Items) != 1 {
		t.Errorf("failed order changed the cart: %+v", cart)
	}
	if page := expect[orderPage](t, ta.do(http.MethodGet, "/api/v1/orders", nil, user), http.StatusOK); page.Pagination.Total != 0 {
		t.Errorf("failed attempts created %d orders", page.Pagination.Total)
	}
}

func TestOrderHistoryIsPrivate(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	ana, bia := ta.signIn("customer"), ta.signIn("customer")

	var anaOrders []string
	for range 3 {
		expect[cartView](t, ta.addToCart(ana, c.iphone128.ID, 1), http.StatusOK)
		anaOrders = append(anaOrders, ta.placeOrder(ana).ID)
	}

	page := expect[orderPage](t, ta.do(http.MethodGet, "/api/v1/orders?per_page=2", nil, ana), http.StatusOK)
	if page.Pagination.Total != 3 || len(page.Data) != 2 || page.Data[0].ID != anaOrders[2] || page.Data[0].ItemCount != 1 {
		t.Errorf("ana's history = %+v", page)
	}
	if page := expect[orderPage](t, ta.do(http.MethodGet, "/api/v1/orders", nil, bia), http.StatusOK); page.Pagination.Total != 0 {
		t.Errorf("bia sees %d orders", page.Pagination.Total)
	}
	// Someone else's order looks exactly like a missing one.
	expectError(t, ta.do(http.MethodGet, "/api/v1/orders/"+anaOrders[0], nil, bia), http.StatusNotFound, "ORDER_NOT_FOUND")
	expectError(t, ta.do(http.MethodPost, "/api/v1/orders/"+anaOrders[0]+"/cancel", nil, bia), http.StatusNotFound, "ORDER_NOT_FOUND")
	expectError(t, ta.do(http.MethodGet, "/api/v1/orders/not-an-id", nil, ana), http.StatusNotFound, "ORDER_NOT_FOUND")

	all := expect[orderPage](t, ta.do(http.MethodGet, "/api/v1/admin/orders?status=pending_payment", nil, c.admin), http.StatusOK)
	if all.Pagination.Total != 3 {
		t.Errorf("admin sees %d pending orders, want 3", all.Pagination.Total)
	}
	expectError(t, ta.do(http.MethodGet, "/api/v1/admin/orders?status=lost", nil, c.admin), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
}

func TestCancelOrderReleasesStock(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")
	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 2), http.StatusOK)
	o := ta.placeOrder(user)

	got := expect[orderView](t, ta.do(http.MethodPost, "/api/v1/orders/"+o.ID+"/cancel", nil, user), http.StatusOK)
	if got.Status != "cancelled" || got.ExpiresAt != nil || len(got.StatusHistory) != 2 || got.StatusHistory[1].Reason != "cancelled by customer" {
		t.Errorf("cancelled order = %+v", got)
	}
	if s := ta.stock(c.admin, c.iphone128.ID); s.Reserved != 0 || s.OnHand != 5 {
		t.Errorf("stock after cancel = %+v", s)
	}
	expectError(t, ta.do(http.MethodPost, "/api/v1/orders/"+o.ID+"/cancel", nil, user), http.StatusConflict, "INVALID_STATUS_TRANSITION")
}

func TestAdminFulfilmentFlow(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")
	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 1), http.StatusOK)
	o := ta.placeOrder(user)
	path := "/api/v1/admin/orders/" + o.ID + "/status"

	expectError(t, ta.do(http.MethodPatch, path, map[string]string{"status": "shipped"}, c.admin), http.StatusConflict, "INVALID_STATUS_TRANSITION")
	expectError(t, ta.do(http.MethodPatch, path, map[string]string{"status": "paid"}, c.admin), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPatch, path, map[string]string{"status": "expired"}, c.admin), http.StatusUnprocessableEntity, "VALIDATION_FAILED")

	// Payment is confirmed by Stripe (STEP 7); simulate it at the database.
	if _, err := ta.db.Exec(context.Background(), `UPDATE orders SET status = 'paid', expires_at = NULL WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	expectError(t, ta.do(http.MethodPost, "/api/v1/orders/"+o.ID+"/cancel", nil, user), http.StatusConflict, "INVALID_STATUS_TRANSITION")

	shipped := expect[orderView](t, ta.do(http.MethodPatch, path, map[string]string{"status": "shipped", "reason": "tracking BR123"}, c.admin), http.StatusOK)
	delivered := expect[orderView](t, ta.do(http.MethodPatch, path, map[string]string{"status": "delivered"}, c.admin), http.StatusOK)
	if shipped.Status != "shipped" || delivered.Status != "delivered" {
		t.Fatalf("statuses = %s, %s", shipped.Status, delivered.Status)
	}
	last := delivered.StatusHistory[len(delivered.StatusHistory)-1]
	if last.From == nil || *last.From != "shipped" || last.To != "delivered" || last.Reason != "set by admin" {
		t.Errorf("last history entry = %+v", last)
	}
	if shipped.StatusHistory[len(shipped.StatusHistory)-1].Reason != "tracking BR123" {
		t.Error("admin reason not recorded")
	}
	expectError(t, ta.do(http.MethodPatch, path, map[string]string{"status": "cancelled"}, c.admin), http.StatusConflict, "INVALID_STATUS_TRANSITION")
}

// Many customers racing for the last units: exactly as many orders succeed
// as there are units, and nothing is oversold.
func TestConcurrentOrdersNeverOversell(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	expect[stockLevel](t, ta.do(http.MethodPut, "/api/v1/variants/"+c.iphone128.ID+"/inventory", map[string]int{"on_hand": 3}, c.admin), http.StatusOK)

	users := make([]string, 8)
	for i := range users {
		users[i] = ta.signIn("customer")
		expect[cartView](t, ta.addToCart(users[i], c.iphone128.ID, 1), http.StatusOK)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses = map[int]int{}
	)
	for _, u := range users {
		wg.Go(func() {
			code := ta.do(http.MethodPost, "/api/v1/orders", nil, u).Code
			mu.Lock()
			statuses[code]++
			mu.Unlock()
		})
	}
	wg.Wait()

	if statuses[http.StatusCreated] != 3 || statuses[http.StatusConflict] != 5 {
		t.Errorf("statuses = %v, want 3 created and 5 conflicts", statuses)
	}
	if s := ta.stock(c.admin, c.iphone128.ID); s != (stockLevel{OnHand: 3, Reserved: 3, Available: 0}) {
		t.Errorf("stock = %+v", s)
	}
}

// Orders that share variants lock inventory rows in the same order, so
// carts filled in opposite orders cannot deadlock each other.
func TestConcurrentOrdersWithSharedVariantsDoNotDeadlock(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	galaxy := expect[product](t, ta.do(http.MethodGet, "/api/v1/products/galaxy-s24", nil, ""), http.StatusOK).Variants[0]
	for _, id := range []string{c.iphone128.ID, galaxy.ID} {
		expect[stockLevel](t, ta.do(http.MethodPut, "/api/v1/variants/"+id+"/inventory", map[string]int{"on_hand": 1000}, c.admin), http.StatusOK)
	}

	users := make([]string, 16)
	for i := range users {
		users[i] = ta.signIn("customer")
		first, second := c.iphone128.ID, galaxy.ID
		if i%2 == 1 {
			first, second = second, first
		}
		expect[cartView](t, ta.addToCart(users[i], first, 1), http.StatusOK)
		expect[cartView](t, ta.addToCart(users[i], second, 1), http.StatusOK)
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed []int
	)
	for _, u := range users {
		wg.Go(func() {
			if code := ta.do(http.MethodPost, "/api/v1/orders", nil, u).Code; code != http.StatusCreated {
				mu.Lock()
				failed = append(failed, code)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(failed) > 0 {
		t.Fatalf("%d orders failed with statuses %v", len(failed), failed)
	}
	if s := ta.stock(c.admin, galaxy.ID); s.Reserved != 16 {
		t.Errorf("galaxy reserved = %d, want 16", s.Reserved)
	}
}
