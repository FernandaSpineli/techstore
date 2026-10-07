package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// Handler exposes the catalog over HTTP.
type Handler struct {
	store *Store
}

// NewHandler returns a Handler.
func NewHandler(store *Store) *Handler { return &Handler{store: store} }

var (
	errCategoryNotFound = httpx.NewError(http.StatusNotFound, "CATEGORY_NOT_FOUND", "Category not found")
	errProductNotFound  = httpx.NewError(http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found")
	errVariantNotFound  = httpx.NewError(http.StatusNotFound, "VARIANT_NOT_FOUND", "Variant not found")
)

func toHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrCategoryNotFound):
		return errCategoryNotFound
	case errors.Is(err, ErrProductNotFound):
		return errProductNotFound
	case errors.Is(err, ErrVariantNotFound):
		return errVariantNotFound
	case errors.Is(err, ErrSlugTaken):
		return httpx.NewError(http.StatusConflict, "SLUG_TAKEN", "Another item already uses this slug")
	case errors.Is(err, ErrSKUTaken):
		return httpx.NewError(http.StatusConflict, "SKU_TAKEN", "Another variant already uses this SKU")
	case errors.Is(err, ErrCategoryInUse):
		return httpx.NewError(http.StatusConflict, "CATEGORY_IN_USE", "Move or delete the category's products first")
	case errors.Is(err, ErrProductHasOrders):
		return httpx.NewError(http.StatusConflict, "PRODUCT_HAS_ORDERS",
			"This product has been ordered and cannot be deleted; archive it instead")
	}
	return err
}

// --- Public -----------------------------------------------------------------

// ListCategories handles GET /api/v1/categories.
func (h *Handler) ListCategories(w http.ResponseWriter, r *http.Request) error {
	cs, err := h.store.ListCategories(r.Context())
	if err != nil {
		return err
	}
	if cs == nil {
		cs = []Category{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": cs})
	return nil
}

// ListProducts handles GET /api/v1/products: active products only.
func (h *Handler) ListProducts(w http.ResponseWriter, r *http.Request) error {
	return h.list(w, r, false)
}

// AdminListProducts handles GET /api/v1/admin/products: every status, with
// an optional ?status= filter.
func (h *Handler) AdminListProducts(w http.ResponseWriter, r *http.Request) error {
	return h.list(w, r, true)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, admin bool) error {
	var p httpx.Problems
	f := parseListFilter(r, &p, admin)
	page := httpx.ParsePage(r, &p)
	if err := p.Err(); err != nil {
		return err
	}

	items, total, err := h.store.ListProducts(r.Context(), f, page)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.NewPaginated(items, page, total))
	return nil
}

func parseListFilter(r *http.Request, p *httpx.Problems, admin bool) ListFilter {
	q := r.URL.Query()
	f := ListFilter{
		Query:    strings.TrimSpace(q.Get("q")),
		Category: q.Get("category"),
		Brand:    strings.TrimSpace(q.Get("brand")),
		Admin:    admin,
	}
	if utf8.RuneCountInString(f.Query) > 100 {
		p.Add("q", "must be at most 100 characters")
	}
	f.MinPrice = parsePrice(q.Get("min_price"), "min_price", p)
	f.MaxPrice = parsePrice(q.Get("max_price"), "max_price", p)
	if f.MinPrice != nil && f.MaxPrice != nil && *f.MinPrice > *f.MaxPrice {
		p.Add("max_price", "must not be lower than min_price")
	}
	if v := q.Get("in_stock"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			p.Add("in_stock", "must be true or false")
		}
		f.InStock = b
	}

	switch s := Sort(q.Get("sort")); s {
	case "":
		f.Sort = SortNewest
		if f.Query != "" {
			f.Sort = SortRelevance
		}
	case SortNewest, SortPriceAsc, SortPriceDesc, SortName:
		f.Sort = s
	case SortRelevance:
		if f.Query == "" {
			p.Add("sort", "relevance requires a search query (q)")
		}
		f.Sort = s
	default:
		p.Add("sort", "must be one of newest, price_asc, price_desc, name, relevance")
	}

	if v := Status(q.Get("status")); v != "" {
		if !admin || !v.valid() {
			p.Add("status", "must be one of draft, active, archived (admin only)")
		}
		f.Status = v
	}
	return f
}

func parsePrice(v, field string, p *httpx.Problems) *int64 {
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		p.Add(field, "must be a non-negative integer amount in cents")
		return nil
	}
	return &n
}

// GetProduct handles GET /api/v1/products/{slug}.
func (h *Handler) GetProduct(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	if !slugPattern.MatchString(slug) {
		return errProductNotFound
	}
	product, err := h.store.ProductBySlug(r.Context(), slug)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, product)
	return nil
}

// --- Admin: categories ------------------------------------------------------

type categoryRequest struct {
	Name *string `json:"name"`
	Slug *string `json:"slug"`
}

func (req *categoryRequest) validate(creating bool) error {
	var p httpx.Problems
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
	}
	switch {
	case req.Name != nil:
		checkLen(&p, "name", *req.Name, 1, 80)
	case creating:
		p.Add("name", "is required")
	}
	if creating && req.Slug == nil && req.Name != nil {
		s := Slugify(*req.Name)
		req.Slug = &s
	}
	checkSlug(&p, req.Slug)
	return p.Err()
}

// CreateCategory handles POST /api/v1/categories.
func (h *Handler) CreateCategory(w http.ResponseWriter, r *http.Request) error {
	var req categoryRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := req.validate(true); err != nil {
		return err
	}
	c, err := h.store.CreateCategory(r.Context(), *req.Name, *req.Slug)
	if err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.category.created", "category_id", c.ID)
	httpx.WriteJSON(w, http.StatusCreated, c)
	return nil
}

// UpdateCategory handles PATCH /api/v1/categories/{id}.
func (h *Handler) UpdateCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errCategoryNotFound)
	if err != nil {
		return err
	}
	var req categoryRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := req.validate(false); err != nil {
		return err
	}
	c, err := h.store.UpdateCategory(r.Context(), id, req.Name, req.Slug)
	if err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.category.updated", "category_id", c.ID)
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

// DeleteCategory handles DELETE /api/v1/categories/{id}.
func (h *Handler) DeleteCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errCategoryNotFound)
	if err != nil {
		return err
	}
	if err := h.store.DeleteCategory(r.Context(), id); err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.category.deleted", "category_id", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- Admin: products --------------------------------------------------------

type productRequest struct {
	CategoryID  *string `json:"category_id"`
	Name        *string `json:"name"`
	Slug        *string `json:"slug"`
	Brand       *string `json:"brand"`
	Description *string `json:"description"`
	Status      *Status `json:"status"`
}

func (req *productRequest) validate(creating bool) error {
	var p httpx.Problems
	trim(req.Name, req.Brand, req.Description)

	switch {
	case req.CategoryID != nil:
		if !httpx.IsUUID(*req.CategoryID) {
			p.Add("category_id", "must be a category ID")
		}
	case creating:
		p.Add("category_id", "is required")
	}
	switch {
	case req.Name != nil:
		checkLen(&p, "name", *req.Name, 1, 200)
	case creating:
		p.Add("name", "is required")
	}
	switch {
	case req.Brand != nil:
		checkLen(&p, "brand", *req.Brand, 1, 80)
	case creating:
		p.Add("brand", "is required")
	}
	if req.Description != nil {
		checkLen(&p, "description", *req.Description, 0, 5000)
	}
	if req.Status != nil && !req.Status.valid() {
		p.Add("status", "must be one of draft, active, archived")
	}
	if creating && req.Slug == nil && req.Name != nil {
		s := Slugify(*req.Name)
		req.Slug = &s
	}
	checkSlug(&p, req.Slug)
	return p.Err()
}

// AdminGetProduct handles GET /api/v1/admin/products/{id}.
func (h *Handler) AdminGetProduct(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errProductNotFound)
	if err != nil {
		return err
	}
	product, err := h.store.ProductByID(r.Context(), id)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, product)
	return nil
}

// CreateProduct handles POST /api/v1/products.
func (h *Handler) CreateProduct(w http.ResponseWriter, r *http.Request) error {
	var req productRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := req.validate(true); err != nil {
		return err
	}
	product, err := h.store.CreateProduct(r.Context(), ProductInput(req))
	if err != nil {
		return toProductWriteError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.product.created", "product_id", product.ID)
	httpx.WriteJSON(w, http.StatusCreated, product)
	return nil
}

// UpdateProduct handles PATCH /api/v1/products/{id}.
func (h *Handler) UpdateProduct(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errProductNotFound)
	if err != nil {
		return err
	}
	var req productRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := req.validate(false); err != nil {
		return err
	}
	product, err := h.store.UpdateProduct(r.Context(), id, ProductInput(req))
	if err != nil {
		return toProductWriteError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.product.updated",
		"product_id", product.ID, "status", string(product.Status))
	httpx.WriteJSON(w, http.StatusOK, product)
	return nil
}

// toProductWriteError reports a missing category as a field problem: the
// product exists, the reference in the body does not.
func toProductWriteError(err error) error {
	if errors.Is(err, ErrCategoryNotFound) {
		var p httpx.Problems
		p.Add("category_id", "does not exist")
		return p.Err()
	}
	return toHTTPError(err)
}

// DeleteProduct handles DELETE /api/v1/products/{id}.
func (h *Handler) DeleteProduct(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errProductNotFound)
	if err != nil {
		return err
	}
	if err := h.store.DeleteProduct(r.Context(), id); err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.product.deleted", "product_id", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- Admin: variants --------------------------------------------------------

type variantRequest struct {
	SKU        *string           `json:"sku"`
	Name       *string           `json:"name"`
	Attributes map[string]string `json:"attributes"`
	PriceCents *int64            `json:"price_cents"`
	Active     *bool             `json:"active"`
}

var skuPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{1,63}$`)

// maxPriceCents (R$ 10 million) rejects obvious typos and keeps every total
// far from integer overflow.
const maxPriceCents = 1_000_000_000

func (req *variantRequest) validate(creating bool) error {
	var p httpx.Problems
	if req.SKU != nil {
		*req.SKU = strings.ToUpper(strings.TrimSpace(*req.SKU))
	}
	trim(req.Name)

	switch {
	case req.SKU != nil:
		if !skuPattern.MatchString(*req.SKU) {
			p.Add("sku", "must be 2-64 characters: letters, digits and dashes")
		}
	case creating:
		p.Add("sku", "is required")
	}
	switch {
	case req.Name != nil:
		checkLen(&p, "name", *req.Name, 1, 120)
	case creating:
		p.Add("name", "is required")
	}
	switch {
	case req.PriceCents != nil:
		if *req.PriceCents <= 0 || *req.PriceCents > maxPriceCents {
			p.Add("price_cents", "must be between 1 and 1000000000")
		}
	case creating:
		p.Add("price_cents", "is required")
	}
	if len(req.Attributes) > 10 {
		p.Add("attributes", "must have at most 10 entries")
	}
	for k, v := range req.Attributes {
		if n, m := utf8.RuneCountInString(k), utf8.RuneCountInString(v); n < 1 || n > 40 || m < 1 || m > 80 {
			p.Add("attributes", fmt.Sprintf("entry %q: keys must be 1-40 and values 1-80 characters", k))
		}
	}
	return p.Err()
}

// CreateVariant handles POST /api/v1/products/{id}/variants.
func (h *Handler) CreateVariant(w http.ResponseWriter, r *http.Request) error {
	productID, err := httpx.PathUUID(r, "id", errProductNotFound)
	if err != nil {
		return err
	}
	var req variantRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := req.validate(true); err != nil {
		return err
	}
	v, err := h.store.CreateVariant(r.Context(), productID, VariantInput(req))
	if err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.variant.created",
		"product_id", productID, "variant_id", v.ID, "sku", v.SKU)
	httpx.WriteJSON(w, http.StatusCreated, v)
	return nil
}

// UpdateVariant handles PATCH /api/v1/variants/{id}.
func (h *Handler) UpdateVariant(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errVariantNotFound)
	if err != nil {
		return err
	}
	var req variantRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := req.validate(false); err != nil {
		return err
	}
	v, err := h.store.UpdateVariant(r.Context(), id, VariantInput(req))
	if err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "catalog.variant.updated", "variant_id", v.ID)
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

// --- Validation helpers -----------------------------------------------------

func trim(fields ...*string) {
	for _, f := range fields {
		if f != nil {
			*f = strings.TrimSpace(*f)
		}
	}
}

func checkLen(p *httpx.Problems, field, v string, minLen, maxLen int) {
	if n := utf8.RuneCountInString(v); n < minLen || n > maxLen {
		p.Add(field, fmt.Sprintf("must be between %d and %d characters", minLen, maxLen))
	}
}

func checkSlug(p *httpx.Problems, slug *string) {
	if slug != nil && (!slugPattern.MatchString(*slug) || len(*slug) > 120) {
		p.Add("slug", "must be lowercase letters, digits and single dashes (max 120)")
	}
}
