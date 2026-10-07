package cart

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// Service implements the cart use cases.
type Service struct {
	db       *pgxpool.Pool
	currency string
}

// NewService returns a Service pricing carts in currency.
func NewService(db *pgxpool.Pool, currency string) *Service {
	return &Service{db: db, currency: currency}
}

// Get returns the user's cart. A user who never added anything gets an
// empty cart; no row is created for reads.
func (s *Service) Get(ctx context.Context, userID string) (Cart, error) {
	items, err := Items(ctx, s.db, userID)
	if err != nil {
		return Cart{}, err
	}
	return summarize(items, s.currency), nil
}

// Add puts quantity more units of a variant in the cart.
func (s *Service) Add(ctx context.Context, userID, variantID string, quantity int) (Cart, error) {
	return s.mutate(ctx, userID, variantID, func(current int, inCart bool) (int, error) {
		return current + quantity, nil
	})
}

// SetQuantity changes the quantity of a line already in the cart.
func (s *Service) SetQuantity(ctx context.Context, userID, variantID string, quantity int) (Cart, error) {
	return s.mutate(ctx, userID, variantID, func(_ int, inCart bool) (int, error) {
		if !inCart {
			return 0, ErrItemNotFound
		}
		return quantity, nil
	})
}

// mutate runs a quantity change for one line inside a transaction. The
// upsert on carts locks the user's cart row, so concurrent requests from
// the same user are serialised and cannot both pass the stock check.
func (s *Service) mutate(ctx context.Context, userID, variantID string, next func(current int, inCart bool) (int, error)) (Cart, error) {
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		cartID, err := lockCart(ctx, tx, userID)
		if err != nil {
			return err
		}

		var purchasable bool
		var available int
		err = tx.QueryRow(ctx, `
			SELECT v.active AND p.status = 'active', COALESCE(i.on_hand - i.reserved, 0)
			FROM product_variants v
			JOIN products p ON p.id = v.product_id
			LEFT JOIN inventory i ON i.variant_id = v.id
			WHERE v.id = $1`, variantID).Scan(&purchasable, &available)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVariantNotFound
		}
		if err != nil {
			return fmt.Errorf("cart: load variant: %w", err)
		}

		var current int
		err = tx.QueryRow(ctx, `SELECT quantity FROM cart_items WHERE cart_id = $1 AND variant_id = $2`,
			cartID, variantID).Scan(&current)
		inCart := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("cart: load item: %w", err)
		}

		quantity, err := next(current, inCart)
		if err != nil {
			return err
		}
		if !purchasable {
			return ErrVariantUnavailable
		}
		if err := checkQuantity(quantity, available); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
			ON CONFLICT (cart_id, variant_id) DO UPDATE SET quantity = EXCLUDED.quantity`,
			cartID, variantID, quantity)
		if err != nil {
			return fmt.Errorf("cart: save item: %w", err)
		}
		return nil
	})
	if err != nil {
		return Cart{}, err
	}
	return s.Get(ctx, userID)
}

// Remove deletes a line from the cart.
func (s *Service) Remove(ctx context.Context, userID, variantID string) (Cart, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM cart_items ci USING carts c
		WHERE ci.cart_id = c.id AND c.user_id = $1 AND ci.variant_id = $2`, userID, variantID)
	if err != nil {
		return Cart{}, fmt.Errorf("cart: remove item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Cart{}, ErrItemNotFound
	}
	return s.Get(ctx, userID)
}

// Clear empties the cart.
func (s *Service) Clear(ctx context.Context, userID string) error {
	return Clear(ctx, s.db, userID)
}

// lockCart returns the user's cart ID, creating the cart if needed, and
// holds a row lock on it until the transaction ends.
func lockCart(ctx context.Context, tx pgx.Tx, userID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO carts (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO UPDATE SET updated_at = now()
		RETURNING id`, userID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("cart: lock cart: %w", err)
	}
	return id, nil
}

// Items loads the cart lines with current prices and availability. It is
// exported so order creation can read the cart inside its own transaction.
func Items(ctx context.Context, q postgres.Querier, userID string) ([]Item, error) {
	rows, err := q.Query(ctx, `
		SELECT ci.variant_id, v.sku, p.name, p.slug, v.name, v.price_cents, ci.quantity,
		       v.active AND p.status = 'active', COALESCE(i.on_hand - i.reserved, 0)
		FROM carts c
		JOIN cart_items ci ON ci.cart_id = c.id
		JOIN product_variants v ON v.id = ci.variant_id
		JOIN products p ON p.id = v.product_id
		LEFT JOIN inventory i ON i.variant_id = v.id
		WHERE c.user_id = $1
		ORDER BY ci.created_at, ci.variant_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("cart: load items: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Item, error) {
		var it Item
		err := row.Scan(&it.VariantID, &it.SKU, &it.ProductName, &it.ProductSlug, &it.VariantName,
			&it.UnitPriceCents, &it.Quantity, &it.purchasable, &it.available)
		return it, err
	})
	if err != nil {
		return nil, fmt.Errorf("cart: scan items: %w", err)
	}
	return items, nil
}

// Clear removes every line from the user's cart. It is exported so order
// creation can empty the cart in the same transaction.
func Clear(ctx context.Context, q postgres.Querier, userID string) error {
	_, err := q.Exec(ctx, `
		DELETE FROM cart_items ci USING carts c
		WHERE ci.cart_id = c.id AND c.user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("cart: clear: %w", err)
	}
	return nil
}

// LockForCheckout locks the user's cart row until the caller's transaction
// ends, so the cart cannot change while an order is created from it. A user
// without a cart has nothing to lock.
func LockForCheckout(ctx context.Context, q postgres.Querier, userID string) error {
	_, err := q.Exec(ctx, `SELECT 1 FROM carts WHERE user_id = $1 FOR UPDATE`, userID)
	if err != nil {
		return fmt.Errorf("cart: lock for checkout: %w", err)
	}
	return nil
}

// Purchasable reports whether the item can be ordered as it is now.
func (it Item) Purchasable() bool { return it.purchasable }
