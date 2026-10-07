package demo

import (
	"context"
	"testing"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres/pgtest"
)

func TestSeedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)

	n, err := Seed(ctx, db)
	if err != nil || n != len(products) {
		t.Fatalf("first seed = %d, %v; want %d", n, err, len(products))
	}
	if n, err := Seed(ctx, db); err != nil || n != 0 {
		t.Fatalf("second seed = %d, %v; want 0", n, err)
	}

	var variants, outOfStock int
	if err := db.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE i.on_hand = 0)
		FROM product_variants v JOIN inventory i ON i.variant_id = v.id`).Scan(&variants, &outOfStock); err != nil {
		t.Fatal(err)
	}
	if variants < len(products) || outOfStock == 0 {
		t.Errorf("%d variants, %d out of stock; want every product stocked and at least one sold out", variants, outOfStock)
	}
}
