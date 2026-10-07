package app_test

import (
	"net/http"
	"testing"
)

func TestCategories(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()

	list := expect[struct{ Data []category }](t, ta.do(http.MethodGet, "/api/v1/categories", nil, ""), http.StatusOK)
	if len(list.Data) != 2 || list.Data[0].Slug != "audio" || list.Data[1].Slug != "phones" {
		t.Errorf("categories = %+v, want audio and phones by name", list.Data)
	}

	got := expect[category](t, ta.do(http.MethodPatch, "/api/v1/categories/"+c.audio.ID,
		map[string]string{"name": "Áudio e Som"}, c.admin), http.StatusOK)
	if got.Name != "Áudio e Som" || got.Slug != "audio" {
		t.Errorf("renamed category = %+v; the slug must not change on rename", got)
	}
	expectError(t, ta.do(http.MethodPatch, "/api/v1/categories/"+c.audio.ID, map[string]string{"slug": "phones"}, c.admin),
		http.StatusConflict, "SLUG_TAKEN")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/categories/"+c.audio.ID, map[string]string{"slug": "Bad Slug"}, c.admin),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPost, "/api/v1/categories", map[string]string{"name": "Phones"}, c.admin),
		http.StatusConflict, "SLUG_TAKEN")
	expectError(t, ta.do(http.MethodPost, "/api/v1/categories", map[string]string{}, c.admin),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/categories/01a11699-7a06-7d19-ace9-45b3da7b647b",
		map[string]string{"name": "Ghost"}, c.admin), http.StatusNotFound, "CATEGORY_NOT_FOUND")

	// The public list reflects the change (the cache was invalidated).
	list = expect[struct{ Data []category }](t, ta.do(http.MethodGet, "/api/v1/categories", nil, ""), http.StatusOK)
	if list.Data[0].Name != "Áudio e Som" {
		t.Errorf("public categories = %+v", list.Data)
	}
}

func TestAdminOrderDetail(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user := ta.signIn("customer")
	expect[cartView](t, ta.addToCart(user, c.iphone128.ID, 1), http.StatusOK)
	o := ta.placeOrder(user)

	got := expect[orderView](t, ta.do(http.MethodGet, "/api/v1/admin/orders/"+o.ID, nil, c.admin), http.StatusOK)
	if got.ID != o.ID || len(got.Items) != 1 {
		t.Errorf("admin order = %+v", got)
	}
	expectError(t, ta.do(http.MethodGet, "/api/v1/admin/orders/01a11699-7a06-7d19-ace9-45b3da7b647b", nil, c.admin),
		http.StatusNotFound, "ORDER_NOT_FOUND")
	expectError(t, ta.do(http.MethodGet, "/api/v1/admin/orders?user_id=nope", nil, c.admin),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
}

func TestAuthRejectsIncompleteRequests(t *testing.T) {
	ta := newTestApp(t)
	for _, path := range []string{"/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		expectError(t, ta.do(http.MethodPost, path, map[string]string{}, ""), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	}
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/login", map[string]string{}, ""), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/password/reset", map[string]string{"new_password": "x"}, ""),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/password/reset", map[string]string{"token": "made-up", "new_password": "long-enough-1"}, ""),
		http.StatusBadRequest, "INVALID_RESET_TOKEN")
	expectError(t, ta.do(http.MethodPost, "/api/v1/auth/password/forgot", map[string]string{"email": "not-an-email"}, ""),
		http.StatusUnprocessableEntity, "VALIDATION_FAILED")
}
