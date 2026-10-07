package order

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres/pgtest"
)

// seedCart creates a customer with qty units of a variant (stock 10) in the
// cart, and returns the user and variant IDs.
func seedCart(t *testing.T, db *pgxpool.Pool, qty int) (userID, variantID string) {
	t.Helper()
	ctx := context.Background()
	err := db.QueryRow(ctx, `
		WITH u AS (INSERT INTO users (email, password_hash, name) VALUES ('ana@example.com', 'x', 'Ana') RETURNING id),
		     c AS (INSERT INTO categories (name, slug) VALUES ('Phones', 'phones') RETURNING id),
		     p AS (INSERT INTO products (category_id, name, slug, brand, status) SELECT id, 'Pixel', 'pixel', 'Google', 'active' FROM c RETURNING id),
		     v AS (INSERT INTO product_variants (product_id, sku, name, price_cents) SELECT id, 'PX-1', '128GB', 100000 FROM p RETURNING id),
		     i AS (INSERT INTO inventory (variant_id, on_hand) SELECT id, 10 FROM v),
		     k AS (INSERT INTO carts (user_id) SELECT id FROM u RETURNING id),
		     ci AS (INSERT INTO cart_items (cart_id, variant_id, quantity) SELECT k.id, v.id, $1 FROM k, v)
		SELECT u.id, v.id FROM u, v`, qty).Scan(&userID, &variantID)
	if err != nil {
		t.Fatal(err)
	}
	return userID, variantID
}

func reserved(t *testing.T, db *pgxpool.Pool, variantID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), `SELECT reserved FROM inventory WHERE variant_id = $1`, variantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExpireStaleReleasesReservations(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	svc := NewService(db, "brl", 30*time.Minute)
	userID, variantID := seedCart(t, db, 3)

	o, err := svc.Create(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got := reserved(t, db, variantID); got != 3 {
		t.Fatalf("reserved = %d, want 3", got)
	}

	// Nothing is due yet.
	if n, err := svc.ExpireStale(ctx, 100); err != nil || n != 0 {
		t.Fatalf("ExpireStale before expiry = %d, %v; want 0", n, err)
	}

	svc.now = func() time.Time { return time.Now().Add(31 * time.Minute) }
	if n, err := svc.ExpireStale(ctx, 100); err != nil || n != 1 {
		t.Fatalf("ExpireStale = %d, %v; want 1", n, err)
	}
	if got := reserved(t, db, variantID); got != 0 {
		t.Errorf("reserved after expiry = %d, want 0", got)
	}
	got, err := svc.Get(ctx, userID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusExpired || got.ExpiresAt != nil || len(got.StatusHistory) != 2 {
		t.Errorf("order = status %s, expires_at %v, %d history entries", got.Status, got.ExpiresAt, len(got.StatusHistory))
	}

	// Expired orders are terminal: a second run finds nothing.
	if n, err := svc.ExpireStale(ctx, 100); err != nil || n != 0 {
		t.Fatalf("second ExpireStale = %d, %v; want 0", n, err)
	}
}

// An order locked by another transaction (e.g. a payment being processed)
// is skipped, not waited on.
func TestExpireStaleSkipsLockedOrders(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	svc := NewService(db, "brl", time.Minute)
	userID, _ := seedCart(t, db, 1)
	o, err := svc.Create(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := Lock(ctx, tx, o.ID); err != nil {
		t.Fatal(err)
	}

	svc.now = func() time.Time { return time.Now().Add(time.Hour) }
	done := make(chan int, 1)
	go func() {
		n, _ := svc.ExpireStale(ctx, 100)
		done <- n
	}()
	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("expired %d locked orders, want 0", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExpireStale blocked on a locked order")
	}
}

func TestRunExpirySweeperStopsWithContext(t *testing.T) {
	db := pgtest.New(t)
	svc := NewService(db, "brl", time.Minute)
	userID, variantID := seedCart(t, db, 2)
	if _, err := svc.Create(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Now().Add(time.Hour) }

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		svc.RunExpirySweeper(ctx, 10*time.Millisecond)
		close(stopped)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for reserved(t, db, variantID) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("sweeper did not release the reservation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("sweeper did not stop after cancellation")
	}
}
