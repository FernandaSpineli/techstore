package app_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

type category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type variant struct {
	ID         string            `json:"id"`
	SKU        string            `json:"sku"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes"`
	PriceCents int64             `json:"price_cents"`
	Active     bool              `json:"active"`
	InStock    bool              `json:"in_stock"`
}

type product struct {
	ID       string    `json:"id"`
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Currency string    `json:"currency"`
	Variants []variant `json:"variants"`
}

type productSummary struct {
	Slug          string `json:"slug"`
	MinPriceCents *int64 `json:"min_price_cents"`
	InStock       bool   `json:"in_stock"`
}

type productPage struct {
	Data       []productSummary `json:"data"`
	Pagination struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		Total      int `json:"total"`
		TotalPages int `json:"total_pages"`
	} `json:"pagination"`
}

type stockLevel struct {
	OnHand    int `json:"on_hand"`
	Reserved  int `json:"reserved"`
	Available int `json:"available"`
}

var userSeq int

// signIn registers a new account with the given role and returns its access
// token. Admins are promoted directly in the database, as the CLI does.
func (ta *testApp) signIn(role string) string {
	ta.t.Helper()
	userSeq++
	email := fmt.Sprintf("%s%d@example.com", role, userSeq)
	ta.register(email, "password123")
	if role == "admin" {
		if _, err := ta.db.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE email = $1`, email); err != nil {
			ta.t.Fatal(err)
		}
	}
	return expect[authResponse](ta.t, ta.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": "password123"}, ""), http.StatusOK).Tokens.AccessToken
}

func (ta *testApp) createCategory(admin, name string) category {
	ta.t.Helper()
	return expect[category](ta.t, ta.do(http.MethodPost, "/api/v1/categories", map[string]string{"name": name}, admin), http.StatusCreated)
}

func (ta *testApp) createProduct(admin string, body map[string]any) product {
	ta.t.Helper()
	return expect[product](ta.t, ta.do(http.MethodPost, "/api/v1/products", body, admin), http.StatusCreated)
}

func (ta *testApp) createVariant(admin, productID, sku string, price int64, stock int) variant {
	ta.t.Helper()
	v := expect[variant](ta.t, ta.do(http.MethodPost, "/api/v1/products/"+productID+"/variants",
		map[string]any{"sku": sku, "name": sku, "price_cents": price}, admin), http.StatusCreated)
	if stock > 0 {
		expect[stockLevel](ta.t, ta.do(http.MethodPut, "/api/v1/variants/"+v.ID+"/inventory",
			map[string]int{"on_hand": stock}, admin), http.StatusOK)
	}
	return v
}

// seededCatalog is a small store:
//
//	iphone-15   Apple    phones  active   128GB R$4999 (5 in stock), 256GB R$5999 (none)
//	galaxy-s24  Samsung  phones  active   R$3999 (3)
//	airpods-pro Apple    audio   active   R$1999 (none)
//	pixel-9     Google   phones  draft    R$4499 (2)
//	old-phone   Nokia    phones  archived R$999  (1)
//	empty-box   Generic  phones  active   no variants
type seededCatalog struct {
	admin           string
	phones, audio   category
	iphone          product
	iphone128, i256 variant
	galaxy, airpods product
	pixel           product
}

func (ta *testApp) seedCatalog() seededCatalog {
	ta.t.Helper()
	var c seededCatalog
	c.admin = ta.signIn("admin")
	c.phones = ta.createCategory(c.admin, "Phones")
	c.audio = ta.createCategory(c.admin, "Audio")

	mk := func(name, brand string, cat category, status string) product {
		return ta.createProduct(c.admin, map[string]any{
			"name": name, "brand": brand, "category_id": cat.ID, "status": status,
		})
	}
	c.iphone = mk("iPhone 15", "Apple", c.phones, "active")
	c.iphone128 = ta.createVariant(c.admin, c.iphone.ID, "IP15-128", 499900, 5)
	c.i256 = ta.createVariant(c.admin, c.iphone.ID, "IP15-256", 599900, 0)
	c.galaxy = mk("Galaxy S24", "Samsung", c.phones, "active")
	ta.createVariant(c.admin, c.galaxy.ID, "GS24-256", 399900, 3)
	c.airpods = mk("AirPods Pro", "Apple", c.audio, "active")
	ta.createVariant(c.admin, c.airpods.ID, "APP-2", 199900, 0)
	c.pixel = mk("Pixel 9", "Google", c.phones, "draft")
	ta.createVariant(c.admin, c.pixel.ID, "PX9-128", 449900, 2)
	old := mk("Old Phone", "Nokia", c.phones, "archived")
	ta.createVariant(c.admin, old.ID, "NOKIA-1", 99900, 1)
	mk("Empty Box", "Generic", c.phones, "active")
	return c
}

func (ta *testApp) listSlugs(query string) []string {
	ta.t.Helper()
	page := expect[productPage](ta.t, ta.do(http.MethodGet, "/api/v1/products"+query, nil, ""), http.StatusOK)
	slugs := make([]string, 0, len(page.Data))
	for _, p := range page.Data {
		slugs = append(slugs, p.Slug)
	}
	return slugs
}

func TestCatalogWritesRequireAdmin(t *testing.T) {
	ta := newTestApp(t)
	customer := ta.signIn("customer")
	someID := "01a11699-7a06-7d19-ace9-45b3da7b647b"

	requests := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/categories"},
		{http.MethodPatch, "/api/v1/categories/" + someID},
		{http.MethodDelete, "/api/v1/categories/" + someID},
		{http.MethodGet, "/api/v1/admin/products"},
		{http.MethodGet, "/api/v1/admin/products/" + someID},
		{http.MethodPost, "/api/v1/products"},
		{http.MethodPatch, "/api/v1/admin/products/" + someID},
		{http.MethodDelete, "/api/v1/admin/products/" + someID},
		{http.MethodPost, "/api/v1/products/" + someID + "/variants"},
		{http.MethodPatch, "/api/v1/variants/" + someID},
		{http.MethodGet, "/api/v1/variants/" + someID + "/inventory"},
		{http.MethodPut, "/api/v1/variants/" + someID + "/inventory"},
	}
	for _, r := range requests {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			expectError(t, ta.do(r.method, r.path, map[string]string{}, ""), http.StatusUnauthorized, "UNAUTHORIZED")
			expectError(t, ta.do(r.method, r.path, map[string]string{}, customer), http.StatusForbidden, "FORBIDDEN")
		})
	}
}

func TestPublicListingShowsOnlyBuyableProducts(t *testing.T) {
	ta := newTestApp(t)
	ta.seedCatalog()

	got := ta.listSlugs("")
	want := []string{"airpods-pro", "galaxy-s24", "iphone-15"} // newest first
	if !slices.Equal(got, want) {
		t.Errorf("slugs = %v, want %v", got, want)
	}

	page := expect[productPage](t, ta.do(http.MethodGet, "/api/v1/products?sort=price_asc", nil, ""), http.StatusOK)
	if p := page.Data[2]; p.Slug != "iphone-15" || *p.MinPriceCents != 499900 || !p.InStock {
		t.Errorf("iphone summary = %+v, want min price 499900 and in stock", p)
	}
	if p := page.Data[0]; p.Slug != "airpods-pro" || p.InStock {
		t.Errorf("airpods summary = %+v, want out of stock", p)
	}
}

func TestListingSearchFiltersAndSorting(t *testing.T) {
	ta := newTestApp(t)
	ta.seedCatalog()

	tests := []struct {
		query string
		want  []string
	}{
		{"?q=iph", []string{"iphone-15"}},
		{"?q=APPLE&sort=name", []string{"airpods-pro", "iphone-15"}},
		{"?q=galaxy%20s2", []string{"galaxy-s24"}},
		{"?q=pixel", []string{}},                                                       // drafts are never searchable publicly
		{"?q=%27%20%7C%20!%20%26", []string{"airpods-pro", "galaxy-s24", "iphone-15"}}, // operators only: no filter
		{"?category=audio", []string{"airpods-pro"}},
		{"?brand=apple&sort=name", []string{"airpods-pro", "iphone-15"}},
		{"?min_price=450000", []string{"iphone-15"}},
		{"?max_price=300000", []string{"airpods-pro"}},
		{"?min_price=300000&max_price=450000", []string{"galaxy-s24"}},
		{"?in_stock=true&sort=name", []string{"galaxy-s24", "iphone-15"}},
		{"?sort=price_asc", []string{"airpods-pro", "galaxy-s24", "iphone-15"}},
		{"?sort=price_desc", []string{"iphone-15", "galaxy-s24", "airpods-pro"}},
		{"?category=does-not-exist", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := ta.listSlugs(tt.query); !slices.Equal(got, tt.want) {
				t.Errorf("slugs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListingPagination(t *testing.T) {
	ta := newTestApp(t)
	ta.seedCatalog()

	first := expect[productPage](t, ta.do(http.MethodGet, "/api/v1/products?sort=name&per_page=2", nil, ""), http.StatusOK)
	second := expect[productPage](t, ta.do(http.MethodGet, "/api/v1/products?sort=name&per_page=2&page=2", nil, ""), http.StatusOK)
	beyond := expect[productPage](t, ta.do(http.MethodGet, "/api/v1/products?page=9", nil, ""), http.StatusOK)

	if len(first.Data) != 2 || first.Pagination.Total != 3 || first.Pagination.TotalPages != 2 {
		t.Errorf("first page = %+v", first)
	}
	if len(second.Data) != 1 || second.Data[0].Slug != "iphone-15" {
		t.Errorf("second page = %+v", second)
	}
	if beyond.Data == nil || len(beyond.Data) != 0 || beyond.Pagination.Total != 3 {
		t.Errorf("page beyond the end = %+v, want empty data and the real total", beyond)
	}
}

func TestListingRejectsInvalidParameters(t *testing.T) {
	ta := newTestApp(t)
	for _, q := range []string{
		"?sort=popularity",
		"?sort=relevance",
		"?min_price=-1",
		"?min_price=500&max_price=100",
		"?per_page=500",
		"?in_stock=maybe",
		"?status=draft", // status filtering is admin-only
	} {
		t.Run(q, func(t *testing.T) {
			expectError(t, ta.do(http.MethodGet, "/api/v1/products"+q, nil, ""), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
		})
	}
}

func TestProductDetail(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()

	// Deactivated variants disappear from the public page.
	expect[variant](t, ta.do(http.MethodPatch, "/api/v1/variants/"+c.i256.ID, map[string]bool{"active": false}, c.admin), http.StatusOK)

	p := expect[product](t, ta.do(http.MethodGet, "/api/v1/products/iphone-15", nil, ""), http.StatusOK)
	if p.Currency != "brl" || len(p.Variants) != 1 || p.Variants[0].SKU != "IP15-128" || !p.Variants[0].InStock {
		t.Errorf("product = %+v", p)
	}

	for _, slug := range []string{"pixel-9", "old-phone", "empty-box", "nope", "Bad%20Slug"} {
		expectError(t, ta.do(http.MethodGet, "/api/v1/products/"+slug, nil, ""), http.StatusNotFound, "PRODUCT_NOT_FOUND")
	}

	// Admins see drafts and every variant by ID.
	admin := expect[product](t, ta.do(http.MethodGet, "/api/v1/admin/products/"+c.iphone.ID, nil, c.admin), http.StatusOK)
	if len(admin.Variants) != 2 {
		t.Errorf("admin view has %d variants, want 2", len(admin.Variants))
	}
	expect[product](t, ta.do(http.MethodGet, "/api/v1/admin/products/"+c.pixel.ID, nil, c.admin), http.StatusOK)
	expectError(t, ta.do(http.MethodGet, "/api/v1/admin/products/not-a-uuid", nil, c.admin), http.StatusNotFound, "PRODUCT_NOT_FOUND")
}

func TestAdminListing(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()

	all := expect[productPage](t, ta.do(http.MethodGet, "/api/v1/admin/products", nil, c.admin), http.StatusOK)
	if all.Pagination.Total != 6 {
		t.Errorf("admin total = %d, want 6", all.Pagination.Total)
	}
	drafts := expect[productPage](t, ta.do(http.MethodGet, "/api/v1/admin/products?status=draft", nil, c.admin), http.StatusOK)
	if len(drafts.Data) != 1 || drafts.Data[0].Slug != "pixel-9" {
		t.Errorf("drafts = %+v", drafts.Data)
	}
	empty := expect[productPage](t, ta.do(http.MethodGet, "/api/v1/admin/products?q=empty", nil, c.admin), http.StatusOK)
	if len(empty.Data) != 1 || empty.Data[0].MinPriceCents != nil {
		t.Errorf("product without variants = %+v, want null min price", empty.Data)
	}
}

func TestProductWrites(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()

	// Slugs are derived from names, accents included.
	p := ta.createProduct(c.admin, map[string]any{"name": "Câmera Instantânea", "brand": "Fujifilm", "category_id": c.phones.ID})
	if p.Slug != "camera-instantanea" || p.Status != "draft" {
		t.Errorf("created product = %+v, want slug camera-instantanea in draft", p)
	}

	e := expectError(t, ta.do(http.MethodPost, "/api/v1/products", map[string]any{"name": ""}, c.admin),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	if ps := e.problems(t); len(ps) < 3 {
		t.Errorf("expected problems for category_id, name and brand: %+v", ps)
	}
	expectError(t, ta.do(http.MethodPost, "/api/v1/products",
		map[string]any{"name": "X", "brand": "Y", "category_id": "01a11699-7a06-7d19-ace9-45b3da7b647b"}, c.admin),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPost, "/api/v1/products",
		map[string]any{"name": "iPhone 15", "brand": "Apple", "category_id": c.phones.ID}, c.admin),
		http.StatusConflict, "SLUG_TAKEN")
	expectError(t, ta.do(http.MethodPost, "/api/v1/products/"+c.galaxy.ID+"/variants",
		map[string]any{"sku": "ip15-128", "name": "dup", "price_cents": 100}, c.admin),
		http.StatusConflict, "SKU_TAKEN")
	expectError(t, ta.do(http.MethodPost, "/api/v1/products/"+c.galaxy.ID+"/variants",
		map[string]any{"sku": "GS24-512", "name": "512GB", "price_cents": 0}, c.admin),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPost, "/api/v1/products/01a11699-7a06-7d19-ace9-45b3da7b647b/variants",
		map[string]any{"sku": "GHOST-1", "name": "ghost", "price_cents": 100}, c.admin),
		http.StatusNotFound, "PRODUCT_NOT_FOUND")

	// Archiving removes a product from the public catalog.
	expect[product](t, ta.do(http.MethodPatch, "/api/v1/admin/products/"+c.galaxy.ID, map[string]string{"status": "archived"}, c.admin), http.StatusOK)
	if slices.Contains(ta.listSlugs(""), "galaxy-s24") {
		t.Error("archived product still listed")
	}

	v := expect[variant](t, ta.do(http.MethodPatch, "/api/v1/variants/"+c.iphone128.ID,
		map[string]any{"price_cents": 459900, "attributes": map[string]string{"storage": "128GB", "color": "Black"}}, c.admin), http.StatusOK)
	if v.PriceCents != 459900 || v.Attributes["color"] != "Black" || v.SKU != "IP15-128" {
		t.Errorf("updated variant = %+v", v)
	}
}

func TestDeleteRules(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	ctx := context.Background()

	expectError(t, ta.do(http.MethodDelete, "/api/v1/categories/"+c.phones.ID, nil, c.admin), http.StatusConflict, "CATEGORY_IN_USE")
	empty := ta.createCategory(c.admin, "Wearables")
	expect[any](t, ta.do(http.MethodDelete, "/api/v1/categories/"+empty.ID, nil, c.admin), http.StatusNoContent)
	expectError(t, ta.do(http.MethodDelete, "/api/v1/categories/"+empty.ID, nil, c.admin), http.StatusNotFound, "CATEGORY_NOT_FOUND")

	// A sold product cannot be hard-deleted.
	var userID string
	if err := ta.db.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.db.Exec(ctx, `
		WITH o AS (INSERT INTO orders (user_id, status, currency, total_cents) VALUES ($1, 'paid', 'brl', 499900) RETURNING id)
		INSERT INTO order_items (order_id, variant_id, product_name, variant_name, sku, unit_price_cents, quantity)
		SELECT o.id, $2, 'iPhone 15', '128GB', 'IP15-128', 499900, 1 FROM o`, userID, c.iphone128.ID); err != nil {
		t.Fatal(err)
	}
	expectError(t, ta.do(http.MethodDelete, "/api/v1/admin/products/"+c.iphone.ID, nil, c.admin), http.StatusConflict, "PRODUCT_HAS_ORDERS")

	expect[any](t, ta.do(http.MethodDelete, "/api/v1/admin/products/"+c.pixel.ID, nil, c.admin), http.StatusNoContent)
	expectError(t, ta.do(http.MethodGet, "/api/v1/admin/products/"+c.pixel.ID, nil, c.admin), http.StatusNotFound, "PRODUCT_NOT_FOUND")
}

func TestInventory(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	path := "/api/v1/variants/" + c.iphone128.ID + "/inventory"

	got := expect[stockLevel](t, ta.do(http.MethodGet, path, nil, c.admin), http.StatusOK)
	if got != (stockLevel{OnHand: 5, Reserved: 0, Available: 5}) {
		t.Errorf("level = %+v", got)
	}

	if _, err := ta.db.Exec(context.Background(), `UPDATE inventory SET reserved = 3 WHERE variant_id = $1`, c.iphone128.ID); err != nil {
		t.Fatal(err)
	}
	expectError(t, ta.do(http.MethodPut, path, map[string]int{"on_hand": 2}, c.admin), http.StatusConflict, "BELOW_RESERVED")
	got = expect[stockLevel](t, ta.do(http.MethodPut, path, map[string]int{"on_hand": 10}, c.admin), http.StatusOK)
	if got != (stockLevel{OnHand: 10, Reserved: 3, Available: 7}) {
		t.Errorf("level after set = %+v", got)
	}

	expectError(t, ta.do(http.MethodPut, path, map[string]int{"on_hand": -1}, c.admin), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPut, path, map[string]any{}, c.admin), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPut, "/api/v1/variants/01a11699-7a06-7d19-ace9-45b3da7b647b/inventory",
		map[string]int{"on_hand": 1}, c.admin), http.StatusNotFound, "VARIANT_NOT_FOUND")
	expectError(t, ta.do(http.MethodGet, "/api/v1/variants/123/inventory", nil, c.admin), http.StatusNotFound, "VARIANT_NOT_FOUND")
}
