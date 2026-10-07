package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/cart"
	"github.com/FernandaSpineli/techstore/internal/inventory"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// Service implements the order use cases.
type Service struct {
	db             *pgxpool.Pool
	currency       string
	reservationTTL time.Duration
	now            func() time.Time
}

// NewService returns a Service. Stock for an unpaid order stays reserved
// for reservationTTL.
func NewService(db *pgxpool.Pool, currency string, reservationTTL time.Duration) *Service {
	return &Service{db: db, currency: currency, reservationTTL: reservationTTL, now: time.Now}
}

// UnavailableItemsError lists the cart lines that block an order.
type UnavailableItemsError struct {
	VariantIDs []string
}

func (e *UnavailableItemsError) Error() string { return ErrCartHasUnavailable.Error() }
func (e *UnavailableItemsError) Unwrap() error { return ErrCartHasUnavailable }

// Create places an order for everything in the user's cart. In a single
// transaction it re-checks every line, reserves the stock, snapshots the
// items and empties the cart; if any step fails nothing changes.
func (s *Service) Create(ctx context.Context, userID string) (Order, error) {
	var orderID string
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		if err := cart.LockForCheckout(ctx, tx, userID); err != nil {
			return err
		}
		items, err := cart.Items(ctx, tx, userID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return ErrCartEmpty
		}

		var unavailable []string
		lines := make([]inventory.Line, len(items))
		var total int64
		for i, it := range items {
			if !it.Purchasable() {
				unavailable = append(unavailable, it.VariantID)
			}
			lines[i] = inventory.Line{VariantID: it.VariantID, Quantity: it.Quantity}
			total += it.UnitPriceCents * int64(it.Quantity)
		}
		if unavailable != nil {
			return &UnavailableItemsError{VariantIDs: unavailable}
		}
		if err := inventory.Reserve(ctx, tx, lines); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO orders (user_id, currency, total_cents, expires_at)
			VALUES ($1, $2, $3, $4) RETURNING id`,
			userID, s.currency, total, s.now().Add(s.reservationTTL)).Scan(&orderID); err != nil {
			return fmt.Errorf("order: insert: %w", err)
		}
		for _, it := range items {
			if _, err := tx.Exec(ctx, `
				INSERT INTO order_items (order_id, variant_id, product_name, variant_name, sku, unit_price_cents, quantity)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				orderID, it.VariantID, it.ProductName, it.VariantName, it.SKU, it.UnitPriceCents, it.Quantity); err != nil {
				return fmt.Errorf("order: insert item: %w", err)
			}
		}
		if err := recordStatus(ctx, tx, orderID, nil, StatusPendingPayment, "order placed"); err != nil {
			return err
		}
		return cart.Clear(ctx, tx, userID)
	})
	if err != nil {
		return Order{}, err
	}

	o, err := load(ctx, s.db, orderID)
	if err != nil {
		return Order{}, err
	}
	logging.FromContext(ctx).InfoContext(ctx, "order.created",
		"order_id", o.ID, "total_cents", o.TotalCents, "items", len(o.Items))
	return o, nil
}

// Get returns one of the user's orders. Someone else's order is reported as
// not found, so order IDs cannot be probed.
func (s *Service) Get(ctx context.Context, userID, orderID string) (Order, error) {
	o, err := load(ctx, s.db, orderID)
	if err != nil {
		return Order{}, err
	}
	if o.UserID != userID {
		return Order{}, ErrNotFound
	}
	return o, nil
}

// AdminGet returns any order.
func (s *Service) AdminGet(ctx context.Context, orderID string) (Order, error) {
	return load(ctx, s.db, orderID)
}

// ListFilter narrows an order listing.
type ListFilter struct {
	UserID string // empty: every user (admin only)
	Status Status // empty: every status
}

// List returns one page of orders, newest first.
func (s *Service) List(ctx context.Context, f ListFilter, page httpx.Page) ([]Summary, int, error) {
	args := []any{nilIfEmpty(f.UserID), nilIfEmpty(string(f.Status))}
	const where = ` WHERE ($1::uuid IS NULL OR o.user_id = $1) AND ($2::text IS NULL OR o.status = $2)`

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM orders o`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("order: count: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT o.id, o.user_id, o.status, o.currency, o.total_cents, o.created_at,
		       (SELECT COALESCE(sum(quantity), 0) FROM order_items WHERE order_id = o.id)
		FROM orders o`+where+`
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $3 OFFSET $4`, append(args, page.PerPage, page.Offset())...)
	if err != nil {
		return nil, 0, fmt.Errorf("order: list: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var o Summary
		err := row.Scan(&o.ID, &o.UserID, &o.Status, &o.Currency, &o.TotalCents, &o.CreatedAt, &o.ItemCount)
		return o, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("order: scan list: %w", err)
	}
	return out, total, nil
}

// Cancel cancels one of the user's unpaid orders and releases its stock.
func (s *Service) Cancel(ctx context.Context, userID, orderID string) (Order, error) {
	return s.transition(ctx, orderID, StatusCancelled, "cancelled by customer", func(tx pgx.Tx, o Order) error {
		if o.UserID != userID {
			return ErrNotFound
		}
		return noOpenCheckout(ctx, tx, o.ID)
	})
}

// AdminSetStatus applies a status chosen by an admin (shipped, delivered or
// cancelled), subject to the state machine.
func (s *Service) AdminSetStatus(ctx context.Context, orderID string, to Status, reason string) (Order, error) {
	if !adminTargets[to] {
		return Order{}, &TransitionError{To: to}
	}
	return s.transition(ctx, orderID, to, reason, func(tx pgx.Tx, o Order) error {
		if to == StatusCancelled {
			return noOpenCheckout(ctx, tx, o.ID)
		}
		return nil
	})
}

// noOpenCheckout refuses to cancel while the customer may still be paying:
// cancelling then would take their money for a cancelled order. Once the
// Stripe session closes (at most 30 minutes) the order can be cancelled.
func noOpenCheckout(ctx context.Context, tx pgx.Tx, orderID string) error {
	var open bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM payments WHERE order_id = $1 AND status = 'pending' AND expires_at > now())`,
		orderID).Scan(&open)
	if err != nil {
		return fmt.Errorf("order: check open checkout: %w", err)
	}
	if open {
		return ErrPaymentInProgress
	}
	return nil
}

// transition moves an order to a new status in its own transaction. check,
// if set, can veto the change after the order is locked.
func (s *Service) transition(ctx context.Context, orderID string, to Status, reason string, check func(pgx.Tx, Order) error) (Order, error) {
	var from Status
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		o, err := Lock(ctx, tx, orderID)
		if err != nil {
			return err
		}
		if check != nil {
			if err := check(tx, o); err != nil {
				return err
			}
		}
		from = o.Status
		return Transition(ctx, tx, o, to, reason)
	})
	if err != nil {
		return Order{}, err
	}
	logging.FromContext(ctx).InfoContext(ctx, "order.status_changed",
		"order_id", orderID, "from", string(from), "to", string(to), "reason", reason)
	return load(ctx, s.db, orderID)
}

// Transition applies a status change to a locked order inside tx, including
// its effect on stock. The payment module uses it to mark orders paid in the
// same transaction that records the payment.
func Transition(ctx context.Context, tx pgx.Tx, o Order, to Status, reason string) error {
	eff, err := effect(o.Status, to)
	if err != nil {
		return err
	}
	switch eff {
	case stockCommit:
		err = inventory.Commit(ctx, tx, o.lines())
	case stockRelease:
		err = inventory.Release(ctx, tx, o.lines())
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE orders SET status = $2,
			expires_at = CASE WHEN $2 = 'pending_payment' THEN expires_at END
		WHERE id = $1`, o.ID, to); err != nil {
		return fmt.Errorf("order: update status: %w", err)
	}
	from := o.Status
	return recordStatus(ctx, tx, o.ID, &from, to, reason)
}

// Lock loads an order and locks its row until tx ends.
func Lock(ctx context.Context, tx pgx.Tx, orderID string) (Order, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM orders WHERE id = $1 FOR UPDATE`, orderID); err != nil {
		return Order{}, fmt.Errorf("order: lock: %w", err)
	}
	return load(ctx, tx, orderID)
}

// ExpireStale expires up to limit unpaid orders whose reservation has run
// out and releases their stock. SKIP LOCKED lets several API instances run
// the sweeper at once without blocking on, or double-processing, the same
// orders. It returns how many orders it expired.
func (s *Service) ExpireStale(ctx context.Context, limit int) (int, error) {
	var expired []string
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM orders
			WHERE status = 'pending_payment' AND expires_at <= $1
			ORDER BY expires_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED`, s.now(), limit)
		if err != nil {
			return fmt.Errorf("order: find stale: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("order: find stale: %w", err)
		}
		for _, id := range ids {
			o, err := load(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := Transition(ctx, tx, o, StatusExpired, "payment window elapsed"); err != nil {
				return err
			}
		}
		expired = ids
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, id := range expired {
		logging.FromContext(ctx).InfoContext(ctx, "order.status_changed",
			"order_id", id, "from", string(StatusPendingPayment), "to", string(StatusExpired), "reason", "payment window elapsed")
	}
	return len(expired), nil
}

// RunExpirySweeper calls ExpireStale every interval until ctx is cancelled.
func (s *Service) RunExpirySweeper(ctx context.Context, interval time.Duration) {
	log := logging.FromContext(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			n, err := s.ExpireStale(ctx, 100)
			switch {
			case err != nil && ctx.Err() == nil:
				log.ErrorContext(ctx, "job.order_expiry.failed", "error", err)
			case n > 0:
				log.InfoContext(ctx, "job.order_expiry.completed", "expired", n,
					"duration_ms", time.Since(start).Milliseconds())
			}
		}
	}
}

// load reads an order with its items and status history.
func load(ctx context.Context, q postgres.Querier, orderID string) (Order, error) {
	var o Order
	err := q.QueryRow(ctx, `
		SELECT id, user_id, status, currency, total_cents, expires_at, created_at, updated_at
		FROM orders WHERE id = $1`, orderID).Scan(
		&o.ID, &o.UserID, &o.Status, &o.Currency, &o.TotalCents, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("order: get: %w", err)
	}

	rows, err := q.Query(ctx, `
		SELECT variant_id, product_name, variant_name, sku, unit_price_cents, quantity, line_total_cents
		FROM order_items WHERE order_id = $1 ORDER BY sku`, orderID)
	if err != nil {
		return Order{}, fmt.Errorf("order: items: %w", err)
	}
	if o.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Item, error) {
		var it Item
		err := row.Scan(&it.VariantID, &it.ProductName, &it.VariantName, &it.SKU, &it.UnitPriceCents, &it.Quantity, &it.LineTotalCents)
		return it, err
	}); err != nil {
		return Order{}, fmt.Errorf("order: scan items: %w", err)
	}

	rows, err = q.Query(ctx, `
		SELECT from_status, to_status, reason, created_at
		FROM order_status_history WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return Order{}, fmt.Errorf("order: history: %w", err)
	}
	if o.StatusHistory, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (StatusChange, error) {
		var c StatusChange
		err := row.Scan(&c.From, &c.To, &c.Reason, &c.At)
		return c, err
	}); err != nil {
		return Order{}, fmt.Errorf("order: scan history: %w", err)
	}
	return o, nil
}

func recordStatus(ctx context.Context, tx pgx.Tx, orderID string, from *Status, to Status, reason string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO order_status_history (order_id, from_status, to_status, reason) VALUES ($1, $2, $3, $4)`,
		orderID, from, to, reason)
	if err != nil {
		return fmt.Errorf("order: record status: %w", err)
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
