package migrations_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres/pgtest"
)

// PostgreSQL error codes asserted by these tests.
const (
	restrictViolation   = "23001"
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
	checkViolation      = "23514"
)

func wantPgCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v, want PostgreSQL error %s", err, code)
	}
	if pgErr.Code != code {
		t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, code)
	}
}

// fixture holds one row of each parent entity, enough to exercise the
// constraints of the dependent tables.
type fixture struct {
	userID, productID, variantID, orderID string
}

func seed(t *testing.T, ctx context.Context, db *pgxpool.Pool) fixture {
	t.Helper()
	var f fixture
	mustScan(t, db.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, name) VALUES ('ana@example.com', 'x', 'Ana')
		RETURNING id`), &f.userID)
	mustExec(t, db, `INSERT INTO categories (name, slug) VALUES ('Phones', 'phones')`)
	mustScan(t, db.QueryRow(ctx, `
		INSERT INTO products (category_id, name, slug, brand, status)
		SELECT id, 'Pixel 9', 'pixel-9', 'Google', 'active' FROM categories WHERE slug = 'phones'
		RETURNING id`), &f.productID)
	mustScan(t, db.QueryRow(ctx, `
		INSERT INTO product_variants (product_id, sku, name, price_cents)
		VALUES ($1, 'PIXEL9-128-BLK', '128GB Black', 499900) RETURNING id`, f.productID), &f.variantID)
	mustExec(t, db, `INSERT INTO inventory (variant_id, on_hand) VALUES ($1, 10)`, f.variantID)
	mustScan(t, db.QueryRow(ctx, `
		INSERT INTO orders (user_id, currency, total_cents, expires_at)
		VALUES ($1, 'brl', 999800, now() + interval '30 minutes') RETURNING id`, f.userID), &f.orderID)
	return f
}

func mustScan(t *testing.T, row interface{ Scan(...any) error }, dest *string) {
	t.Helper()
	if err := row.Scan(dest); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func mustExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func TestSchemaConstraints(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	f := seed(t, ctx, db)

	tests := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{
			name: "email is unique regardless of case",
			sql:  `INSERT INTO users (email, password_hash, name) VALUES ('ANA@example.com', 'x', 'Other')`,
			code: uniqueViolation,
		},
		{
			name: "unknown role is rejected",
			sql:  `INSERT INTO users (email, password_hash, name, role) VALUES ('bob@example.com', 'x', 'Bob', 'root')`,
			code: checkViolation,
		},
		{
			name: "category slug must be URL-safe",
			sql:  `INSERT INTO categories (name, slug) VALUES ('Audio', 'Audio & Video')`,
			code: checkViolation,
		},
		{
			name: "variant price must be positive",
			sql:  `INSERT INTO product_variants (product_id, sku, name, price_cents) VALUES ($1, 'PIXEL9-256', '256GB', 0)`,
			args: []any{f.productID},
			code: checkViolation,
		},
		{
			name: "SKU is unique",
			sql:  `INSERT INTO product_variants (product_id, sku, name, price_cents) VALUES ($1, 'PIXEL9-128-BLK', 'dup', 100)`,
			args: []any{f.productID},
			code: uniqueViolation,
		},
		{
			name: "reserved stock cannot exceed stock on hand",
			sql:  `UPDATE inventory SET reserved = 11 WHERE variant_id = $1`,
			args: []any{f.variantID},
			code: checkViolation,
		},
		{
			name: "stock on hand cannot go negative",
			sql:  `UPDATE inventory SET on_hand = -1 WHERE variant_id = $1`,
			args: []any{f.variantID},
			code: checkViolation,
		},
		{
			name: "cart quantity must be between 1 and 99",
			sql: `WITH c AS (INSERT INTO carts (user_id) VALUES ($1) RETURNING id)
			      INSERT INTO cart_items (cart_id, variant_id, quantity) SELECT id, $2, 100 FROM c`,
			args: []any{f.userID, f.variantID},
			code: checkViolation,
		},
		{
			name: "pending order needs an expiry for its reservation",
			sql:  `INSERT INTO orders (user_id, currency, total_cents) VALUES ($1, 'brl', 100)`,
			args: []any{f.userID},
			code: checkViolation,
		},
		{
			name: "order cannot reference a missing user",
			sql:  `INSERT INTO orders (user_id, status, currency, total_cents) VALUES (uuidv7(), 'paid', 'brl', 100)`,
			code: foreignKeyViolation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.Exec(ctx, tt.sql, tt.args...)
			wantPgCode(t, err, tt.code)
		})
	}
}

func TestOrderItemsSnapshotAndRestrictVariantDeletion(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	f := seed(t, ctx, db)

	var lineTotal int64
	err := db.QueryRow(ctx, `
		INSERT INTO order_items (order_id, variant_id, product_name, variant_name, sku, unit_price_cents, quantity)
		VALUES ($1, $2, 'Pixel 9', '128GB Black', 'PIXEL9-128-BLK', 499900, 2)
		RETURNING line_total_cents`, f.orderID, f.variantID).Scan(&lineTotal)
	if err != nil {
		t.Fatal(err)
	}
	if lineTotal != 999800 {
		t.Errorf("line_total_cents = %d, want 999800", lineTotal)
	}

	// A product that has been sold cannot be hard-deleted: its variants are
	// still referenced by order history.
	_, err = db.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
	wantPgCode(t, err, restrictViolation)
}

func TestPaymentsAllowOneOpenAndOneSuccessfulPaymentPerOrder(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	f := seed(t, ctx, db)

	insert := func(session, status string) error {
		_, err := db.Exec(ctx, `
			INSERT INTO payments (order_id, stripe_checkout_session_id, status, amount_cents, currency)
			VALUES ($1, $2, $3, 999800, 'brl')`, f.orderID, session, status)
		return err
	}

	if err := insert("cs_1", "pending"); err != nil {
		t.Fatal(err)
	}
	wantPgCode(t, insert("cs_2", "pending"), uniqueViolation)

	// An expired session frees the slot for a new checkout attempt.
	mustExec(t, db, `UPDATE payments SET status = 'expired' WHERE stripe_checkout_session_id = 'cs_1'`)
	if err := insert("cs_2", "succeeded"); err != nil {
		t.Fatal(err)
	}
	wantPgCode(t, insert("cs_3", "succeeded"), uniqueViolation)
}

func TestStripeEventsAreRecordedOnce(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)

	const insert = `INSERT INTO stripe_events (id, type) VALUES ('evt_1', 'checkout.session.completed') ON CONFLICT DO NOTHING`
	for i, want := range []int64{1, 0} {
		tag, err := db.Exec(ctx, insert)
		if err != nil {
			t.Fatal(err)
		}
		if tag.RowsAffected() != want {
			t.Errorf("delivery %d: rows affected = %d, want %d", i+1, tag.RowsAffected(), want)
		}
	}
}

func TestUpdatedAtIsMaintainedByTrigger(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	f := seed(t, ctx, db)

	var before, after time.Time
	if err := db.QueryRow(ctx, `SELECT updated_at FROM users WHERE id = $1`, f.userID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx,
		`UPDATE users SET name = 'Ana Maria' WHERE id = $1 RETURNING updated_at`, f.userID,
	).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Errorf("updated_at did not advance: before %v, after %v", before, after)
	}
}
