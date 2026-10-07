package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// Store runs the catalog's SQL.
type Store struct {
	db *pgxpool.Pool
}

// NewStore returns a Store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// --- Categories -------------------------------------------------------------

const categoryColumns = `id, name, slug, created_at, updated_at`

func scanCategory(row pgx.Row) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.Name, &c.Slug, &c.CreatedAt, &c.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Category{}, ErrCategoryNotFound
	case postgres.IsUniqueViolation(err, "categories_slug_key"):
		return Category{}, ErrSlugTaken
	case err != nil:
		return Category{}, fmt.Errorf("catalog: category: %w", err)
	}
	return c, nil
}

// ListCategories returns every category ordered by name. The set is small
// enough not to need pagination.
func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.db.Query(ctx, `SELECT `+categoryColumns+` FROM categories ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("catalog: list categories: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Category, error) { return scanCategory(row) })
}

// CreateCategory inserts a category.
func (s *Store) CreateCategory(ctx context.Context, name, slug string) (Category, error) {
	return scanCategory(s.db.QueryRow(ctx,
		`INSERT INTO categories (name, slug) VALUES ($1, $2) RETURNING `+categoryColumns, name, slug))
}

// UpdateCategory changes the non-nil fields.
func (s *Store) UpdateCategory(ctx context.Context, id string, name, slug *string) (Category, error) {
	return scanCategory(s.db.QueryRow(ctx, `
		UPDATE categories SET name = COALESCE($2, name), slug = COALESCE($3, slug)
		WHERE id = $1 RETURNING `+categoryColumns, id, name, slug))
}

// DeleteCategory deletes a category that has no products.
func (s *Store) DeleteCategory(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM categories WHERE id = $1`, id)
	switch {
	case postgres.IsRestrictViolation(err, "products_category_id_fkey"):
		return ErrCategoryInUse
	case err != nil:
		return fmt.Errorf("catalog: delete category: %w", err)
	case tag.RowsAffected() == 0:
		return ErrCategoryNotFound
	}
	return nil
}

// --- Product listing --------------------------------------------------------

// Sort orders a product listing.
type Sort string

const (
	SortNewest    Sort = "newest"
	SortPriceAsc  Sort = "price_asc"
	SortPriceDesc Sort = "price_desc"
	SortName      Sort = "name"
	SortRelevance Sort = "relevance" // only with a search query
)

// ListFilter narrows a product listing. Zero values mean "no filter".
type ListFilter struct {
	Query    string
	Category string // slug
	Brand    string
	MinPrice *int64
	MaxPrice *int64
	InStock  bool
	Sort     Sort

	// Admin listings see every status (optionally filtered) and products
	// without active variants; public listings see only what can be bought.
	Admin  bool
	Status Status
}

// queryBuilder accumulates positional arguments for a dynamic query.
type queryBuilder struct {
	args  []any
	where []string
}

func (b *queryBuilder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

func (b *queryBuilder) cond(c string) { b.where = append(b.where, c) }

// listFrom joins each product with its category and an aggregate over its
// active variants: the cheapest price and whether any unit is available.
const listFrom = `
	FROM products p
	JOIN categories c ON c.id = p.category_id
	LEFT JOIN LATERAL (
		SELECT min(v.price_cents) AS min_price,
		       COALESCE(bool_or(i.on_hand - i.reserved > 0), false) AS in_stock
		FROM product_variants v
		LEFT JOIN inventory i ON i.variant_id = v.id
		WHERE v.product_id = p.id AND v.active
	) agg ON true`

// ListProducts returns one page of products matching f, plus the total
// number of matches.
func (s *Store) ListProducts(ctx context.Context, f ListFilter, page httpx.Page) ([]ProductSummary, int, error) {
	var b queryBuilder

	if f.Admin {
		if f.Status != "" {
			b.cond("p.status = " + b.arg(f.Status))
		}
	} else {
		b.cond("p.status = 'active'")
		b.cond("agg.min_price IS NOT NULL")
	}

	var tsquery string
	if q := prefixQuery(f.Query); q != "" {
		tsquery = b.arg(q)
		b.cond("p.search @@ to_tsquery('simple', " + tsquery + ")")
	}
	if f.Category != "" {
		b.cond("c.slug = " + b.arg(f.Category))
	}
	if f.Brand != "" {
		b.cond("lower(p.brand) = lower(" + b.arg(f.Brand) + ")")
	}
	if f.MinPrice != nil || f.MaxPrice != nil {
		// Matches if any single active variant falls inside the range.
		price := []string{"v.product_id = p.id", "v.active"}
		if f.MinPrice != nil {
			price = append(price, "v.price_cents >= "+b.arg(*f.MinPrice))
		}
		if f.MaxPrice != nil {
			price = append(price, "v.price_cents <= "+b.arg(*f.MaxPrice))
		}
		b.cond("EXISTS (SELECT 1 FROM product_variants v WHERE " + strings.Join(price, " AND ") + ")")
	}
	if f.InStock {
		b.cond("agg.in_stock")
	}

	where := ""
	if len(b.where) > 0 {
		where = " WHERE " + strings.Join(b.where, " AND ")
	}

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)`+listFrom+where, b.args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("catalog: count products: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	// Every ordering ends with p.id so pages are stable when values tie.
	orderBy := map[Sort]string{
		SortNewest:    "p.created_at DESC, p.id DESC",
		SortPriceAsc:  "agg.min_price ASC NULLS LAST, p.id",
		SortPriceDesc: "agg.min_price DESC NULLS LAST, p.id",
		SortName:      "p.name, p.id",
	}[f.Sort]
	if f.Sort == SortRelevance && tsquery != "" {
		orderBy = "ts_rank(p.search, to_tsquery('simple', " + tsquery + ")) DESC, p.id"
	}
	if orderBy == "" {
		orderBy = "p.created_at DESC, p.id DESC"
	}

	query := `
		SELECT p.id, p.slug, p.name, p.brand, p.status, p.created_at,
		       c.id, c.slug, c.name, agg.min_price, agg.in_stock` +
		listFrom + where +
		" ORDER BY " + orderBy +
		" LIMIT " + b.arg(page.PerPage) + " OFFSET " + b.arg(page.Offset())

	rows, err := s.db.Query(ctx, query, b.args...)
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: list products: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProductSummary, error) {
		p := ProductSummary{Currency: Currency}
		err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Brand, &p.Status, &p.CreatedAt,
			&p.Category.ID, &p.Category.Slug, &p.Category.Name, &p.MinPriceCents, &p.InStock)
		return p, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: scan products: %w", err)
	}
	return items, total, nil
}

// --- Product detail and writes ----------------------------------------------

const productSelect = `
	SELECT p.id, p.slug, p.name, p.brand, p.description, p.status, p.created_at, p.updated_at,
	       c.id, c.slug, c.name
	FROM products p JOIN categories c ON c.id = p.category_id`

// ProductBySlug returns an active product with its active variants: the
// public product page.
func (s *Store) ProductBySlug(ctx context.Context, slug string) (Product, error) {
	return s.product(ctx, productSelect+` WHERE p.slug = $1 AND p.status = 'active'`, slug, true)
}

// ProductByID returns a product in any status with all its variants: the
// admin view.
func (s *Store) ProductByID(ctx context.Context, id string) (Product, error) {
	return s.product(ctx, productSelect+` WHERE p.id = $1`, id, false)
}

func (s *Store) product(ctx context.Context, query string, arg string, activeVariantsOnly bool) (Product, error) {
	p := Product{Currency: Currency}
	err := s.db.QueryRow(ctx, query, arg).Scan(
		&p.ID, &p.Slug, &p.Name, &p.Brand, &p.Description, &p.Status, &p.CreatedAt, &p.UpdatedAt,
		&p.Category.ID, &p.Category.Slug, &p.Category.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrProductNotFound
	}
	if err != nil {
		return Product{}, fmt.Errorf("catalog: product: %w", err)
	}

	if p.Variants, err = s.variants(ctx, p.ID, activeVariantsOnly); err != nil {
		return Product{}, err
	}
	if !activeVariantsOnly || len(p.Variants) > 0 {
		return p, nil
	}
	// An active product with nothing to sell is not shown publicly.
	return Product{}, ErrProductNotFound
}

const variantColumns = `v.id, v.sku, v.name, v.attributes, v.price_cents, v.active,
	COALESCE(i.on_hand - i.reserved > 0, false)`

func scanVariant(row pgx.Row) (Variant, error) {
	var v Variant
	err := row.Scan(&v.ID, &v.SKU, &v.Name, &v.Attributes, &v.PriceCents, &v.Active, &v.InStock)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Variant{}, ErrVariantNotFound
	case postgres.IsUniqueViolation(err, "product_variants_sku_key"):
		return Variant{}, ErrSKUTaken
	case postgres.IsForeignKeyViolation(err, "product_variants_product_id_fkey"):
		return Variant{}, ErrProductNotFound
	case err != nil:
		return Variant{}, fmt.Errorf("catalog: variant: %w", err)
	}
	return v, nil
}

func (s *Store) variants(ctx context.Context, productID string, activeOnly bool) ([]Variant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+variantColumns+`
		FROM product_variants v LEFT JOIN inventory i ON i.variant_id = v.id
		WHERE v.product_id = $1 AND (v.active OR NOT $2)
		ORDER BY v.price_cents, v.sku`, productID, activeOnly)
	if err != nil {
		return nil, fmt.Errorf("catalog: list variants: %w", err)
	}
	vs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Variant, error) { return scanVariant(row) })
	if vs == nil {
		vs = []Variant{}
	}
	return vs, err
}

// ProductInput holds the fields of a product write. On update, nil fields
// are left unchanged.
type ProductInput struct {
	CategoryID  *string
	Name        *string
	Slug        *string
	Brand       *string
	Description *string
	Status      *Status
}

func productWriteErr(err error) error {
	switch {
	case postgres.IsUniqueViolation(err, "products_slug_key"):
		return ErrSlugTaken
	case postgres.IsForeignKeyViolation(err, "products_category_id_fkey"):
		return ErrCategoryNotFound
	case errors.Is(err, pgx.ErrNoRows):
		return ErrProductNotFound
	}
	return fmt.Errorf("catalog: write product: %w", err)
}

// CreateProduct inserts a product. Name, slug, brand and category are
// required; the handler validates them.
func (s *Store) CreateProduct(ctx context.Context, in ProductInput) (Product, error) {
	status := StatusDraft
	if in.Status != nil {
		status = *in.Status
	}
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	var id string
	err := s.db.QueryRow(ctx, `
		INSERT INTO products (category_id, name, slug, brand, description, status)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		in.CategoryID, in.Name, in.Slug, in.Brand, description, status).Scan(&id)
	if err != nil {
		return Product{}, productWriteErr(err)
	}
	return s.ProductByID(ctx, id)
}

// UpdateProduct changes the non-nil fields.
func (s *Store) UpdateProduct(ctx context.Context, id string, in ProductInput) (Product, error) {
	_, err := s.db.Exec(ctx, `
		UPDATE products SET
			category_id = COALESCE($2, category_id),
			name        = COALESCE($3, name),
			slug        = COALESCE($4, slug),
			brand       = COALESCE($5, brand),
			description = COALESCE($6, description),
			status      = COALESCE($7, status)
		WHERE id = $1`,
		id, in.CategoryID, in.Name, in.Slug, in.Brand, in.Description, in.Status)
	if err != nil {
		return Product{}, productWriteErr(err)
	}
	return s.ProductByID(ctx, id)
}

// DeleteProduct removes a product that has never been ordered, together
// with its variants. Sold products must be archived instead, so order
// history keeps pointing at real rows.
func (s *Store) DeleteProduct(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	switch {
	case postgres.IsRestrictViolation(err, "order_items_variant_id_fkey"):
		return ErrProductHasOrders
	case err != nil:
		return fmt.Errorf("catalog: delete product: %w", err)
	case tag.RowsAffected() == 0:
		return ErrProductNotFound
	}
	return nil
}

// VariantInput holds the fields of a variant write. On update, nil fields
// are left unchanged.
type VariantInput struct {
	SKU        *string
	Name       *string
	Attributes map[string]string
	PriceCents *int64
	Active     *bool
}

// CreateVariant adds a variant to a product. It starts with no stock.
func (s *Store) CreateVariant(ctx context.Context, productID string, in VariantInput) (Variant, error) {
	attrs := in.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	return scanVariant(s.db.QueryRow(ctx, `
		WITH v AS (
			INSERT INTO product_variants (product_id, sku, name, attributes, price_cents, active)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING *
		)
		SELECT `+variantColumns+` FROM v LEFT JOIN inventory i ON i.variant_id = v.id`,
		productID, in.SKU, in.Name, attrs, in.PriceCents, active))
}

// UpdateVariant changes the non-nil fields. A non-nil Attributes replaces
// the whole set.
func (s *Store) UpdateVariant(ctx context.Context, id string, in VariantInput) (Variant, error) {
	var attrs any
	if in.Attributes != nil {
		attrs = in.Attributes
	}
	return scanVariant(s.db.QueryRow(ctx, `
		WITH v AS (
			UPDATE product_variants SET
				sku         = COALESCE($2, sku),
				name        = COALESCE($3, name),
				attributes  = COALESCE($4, attributes),
				price_cents = COALESCE($5, price_cents),
				active      = COALESCE($6, active)
			WHERE id = $1
			RETURNING *
		)
		SELECT `+variantColumns+` FROM v LEFT JOIN inventory i ON i.variant_id = v.id`,
		id, in.SKU, in.Name, attrs, in.PriceCents, in.Active))
}
