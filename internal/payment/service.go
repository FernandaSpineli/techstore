package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"

	"github.com/FernandaSpineli/techstore/internal/order"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

const (
	// sessionTTL is how long the hosted checkout stays open. Stripe requires
	// at least 30 minutes from the moment it creates the session, which is
	// slightly after this timestamp is computed, so keep a margin.
	sessionTTL = 31 * time.Minute
	// reservationGrace keeps the stock reserved a little past the session's
	// end, so a payment completed at the last second still finds it.
	reservationGrace = 5 * time.Minute
	// reuseMargin: an open session closer than this to expiring is replaced
	// rather than handed back.
	reuseMargin = 2 * time.Minute
)

var (
	ErrDisabled         = errors.New("payment: payments are not configured")
	ErrOrderNotPayable  = errors.New("payment: order is not awaiting payment")
	ErrProvider         = errors.New("payment: payment provider error")
	ErrInvalidSignature = errors.New("payment: invalid webhook signature")
	ErrCheckoutConflict = errors.New("payment: another checkout is being created")
)

// Service implements checkout and webhook processing.
type Service struct {
	db            *pgxpool.Pool
	gateway       Gateway // nil when payments are not configured
	webhookSecret string
	baseURL       string
	now           func() time.Time
}

// NewService returns a Service. gateway may be nil, in which case checkout
// and webhooks report ErrDisabled.
func NewService(db *pgxpool.Pool, gateway Gateway, webhookSecret, baseURL string) *Service {
	return &Service{
		db: db, gateway: gateway, webhookSecret: webhookSecret,
		baseURL: strings.TrimRight(baseURL, "/"), now: time.Now,
	}
}

// Checkout is a hosted payment page for an order.
type Checkout struct {
	SessionID string    `json:"session_id"`
	URL       string    `json:"checkout_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// StartCheckout returns a hosted checkout page for one of the user's unpaid
// orders. An open session is reused; otherwise a new one is created. The
// bool reports whether a session was created.
func (s *Service) StartCheckout(ctx context.Context, userID, orderID string) (Checkout, bool, error) {
	if s.gateway == nil {
		return Checkout{}, false, ErrDisabled
	}

	// Phase 1, under the order lock: validate, reuse an open session, or work
	// out the attempt number that keys the new session.
	var (
		o       order.Order
		email   string
		attempt int
		reuse   *Checkout
	)
	err := postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		if o, err = s.payableOrder(ctx, tx, userID, orderID); err != nil {
			return err
		}
		var open Checkout
		err = tx.QueryRow(ctx, `
			SELECT stripe_checkout_session_id, checkout_url, expires_at
			FROM payments WHERE order_id = $1 AND status = 'pending'`, orderID).Scan(&open.SessionID, &open.URL, &open.ExpiresAt)
		switch {
		case err == nil && open.ExpiresAt.After(s.now().Add(reuseMargin)):
			reuse = &open
			return nil
		case err == nil:
			// About to close (or closed without a webhook yet): retire it.
			if _, err := tx.Exec(ctx, `UPDATE payments SET status = 'expired' WHERE stripe_checkout_session_id = $1`, open.SessionID); err != nil {
				return fmt.Errorf("payment: retire session: %w", err)
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("payment: load open session: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) + 1 FROM payments WHERE order_id = $1`, orderID).Scan(&attempt); err != nil {
			return fmt.Errorf("payment: count attempts: %w", err)
		}
		return tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email)
	})
	if err != nil {
		return Checkout{}, false, err
	}
	log := logging.FromContext(ctx)
	if reuse != nil {
		log.InfoContext(ctx, "payment.checkout_reused", "order_id", orderID, "session_id", reuse.SessionID)
		return *reuse, false, nil
	}

	// The Stripe call happens outside any transaction. Concurrent requests
	// compute the same attempt number, hence the same idempotency key, and
	// Stripe hands them the same session.
	req := CheckoutRequest{
		OrderID:        o.ID,
		CustomerEmail:  email,
		Currency:       o.Currency,
		SuccessURL:     s.baseURL + "/checkout/success?order_id=" + url.QueryEscape(o.ID),
		CancelURL:      s.baseURL + "/checkout/cancel?order_id=" + url.QueryEscape(o.ID),
		ExpiresAt:      s.now().Add(sessionTTL),
		IdempotencyKey: fmt.Sprintf("checkout-%s-%d", o.ID, attempt),
	}
	for _, it := range o.Items {
		req.Lines = append(req.Lines, CheckoutLine{
			Name: it.ProductName + " — " + it.VariantName, UnitPriceCents: it.UnitPriceCents, Quantity: it.Quantity,
		})
	}
	sess, err := s.gateway.CreateCheckoutSession(ctx, req)
	if err != nil {
		log.ErrorContext(ctx, "stripe.request_failed", "operation", "checkout.sessions.create", "order_id", o.ID, "error", err)
		return Checkout{}, false, fmt.Errorf("%w: %w", ErrProvider, err)
	}

	// Phase 2: record the session, unless the order stopped being payable
	// meanwhile (the customer never sees that session's URL, so it expires
	// unused).
	err = postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := s.payableOrder(ctx, tx, userID, orderID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO payments (order_id, stripe_checkout_session_id, amount_cents, currency, checkout_url, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (stripe_checkout_session_id) DO NOTHING`,
			o.ID, sess.ID, o.TotalCents, o.Currency, sess.URL, sess.ExpiresAt)
		if postgres.IsUniqueViolation(err, "payments_one_pending_per_order") {
			return ErrCheckoutConflict
		}
		if err != nil {
			return fmt.Errorf("payment: record session: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE orders SET expires_at = GREATEST(expires_at, $2)
			WHERE id = $1 AND status = 'pending_payment'`, o.ID, sess.ExpiresAt.Add(reservationGrace))
		if err != nil {
			return fmt.Errorf("payment: extend reservation: %w", err)
		}
		return nil
	})
	if err != nil {
		return Checkout{}, false, err
	}
	log.InfoContext(ctx, "payment.checkout_created",
		"order_id", o.ID, "session_id", sess.ID, "amount_cents", o.TotalCents, "attempt", attempt)
	return Checkout{SessionID: sess.ID, URL: sess.URL, ExpiresAt: sess.ExpiresAt}, true, nil
}

// payableOrder locks the order and checks that the user may pay for it now.
func (s *Service) payableOrder(ctx context.Context, tx pgx.Tx, userID, orderID string) (order.Order, error) {
	o, err := order.Lock(ctx, tx, orderID)
	if err != nil {
		return order.Order{}, err
	}
	if o.UserID != userID {
		return order.Order{}, order.ErrNotFound
	}
	if o.Status != order.StatusPendingPayment {
		return order.Order{}, ErrOrderNotPayable
	}
	return o, nil
}

// Checkout Session events this service acts on.
const (
	eventCompleted          = "checkout.session.completed"
	eventAsyncSucceeded     = "checkout.session.async_payment_succeeded"
	eventAsyncFailed        = "checkout.session.async_payment_failed"
	eventExpired            = "checkout.session.expired"
	maxWebhookTolerance     = webhook.DefaultTolerance
	paymentStatusPaid       = stripe.CheckoutSessionPaymentStatusPaid
	paymentStatusNoRequired = stripe.CheckoutSessionPaymentStatusNoPaymentRequired
)

// HandleWebhook verifies and applies a Stripe webhook. Each event is
// recorded in stripe_events in the same transaction as its effects, so a
// redelivered event is acknowledged without being applied twice, and a
// failure rolls everything back so Stripe's retry can try again.
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signature string) error {
	if s.gateway == nil {
		return ErrDisabled
	}
	log := logging.FromContext(ctx)

	event, err := webhook.ConstructEventWithOptions(payload, signature, s.webhookSecret, webhook.ConstructEventOptions{
		Tolerance: maxWebhookTolerance,
		// Only a handful of stable Checkout Session fields are read, so an
		// account pinned to a different API version is fine.
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		log.WarnContext(ctx, "webhook.signature_invalid", "error", err)
		return ErrInvalidSignature
	}
	log = log.With("stripe_event_id", event.ID, "stripe_event_type", string(event.Type))
	ctx = logging.WithLogger(ctx, log)

	switch string(event.Type) {
	case eventCompleted, eventAsyncSucceeded, eventAsyncFailed, eventExpired:
	default:
		log.DebugContext(ctx, "webhook.ignored")
		return nil
	}
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		return fmt.Errorf("payment: decode checkout session: %w", err)
	}
	log.InfoContext(ctx, "webhook.received", "session_id", sess.ID)

	return postgres.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO stripe_events (id, type) VALUES ($1, $2) ON CONFLICT DO NOTHING`, event.ID, event.Type)
		if err != nil {
			return fmt.Errorf("payment: record event: %w", err)
		}
		if tag.RowsAffected() == 0 {
			log.InfoContext(ctx, "webhook.duplicate")
			return nil
		}

		p, err := lockPayment(ctx, tx, sess.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Not ours (e.g. another integration on the same account). Keep
			// the event recorded so Stripe stops retrying.
			log.WarnContext(ctx, "webhook.unknown_session", "session_id", sess.ID)
			return nil
		}
		if err != nil {
			return err
		}

		switch string(event.Type) {
		case eventCompleted:
			if sess.PaymentStatus != paymentStatusPaid && sess.PaymentStatus != paymentStatusNoRequired {
				// Delayed methods such as boleto: the outcome comes later as
				// async_payment_succeeded or async_payment_failed.
				log.InfoContext(ctx, "payment.awaiting_confirmation", "order_id", p.orderID, "payment_status", string(sess.PaymentStatus))
				return nil
			}
			return s.succeed(ctx, tx, p, &sess)
		case eventAsyncSucceeded:
			return s.succeed(ctx, tx, p, &sess)
		case eventAsyncFailed:
			return s.fail(ctx, tx, p)
		default: // eventExpired
			return expire(ctx, tx, p)
		}
	})
}

type paymentRow struct {
	id, orderID, status, currency string
	amountCents                   int64
}

func lockPayment(ctx context.Context, tx pgx.Tx, sessionID string) (paymentRow, error) {
	var p paymentRow
	err := tx.QueryRow(ctx, `
		SELECT id, order_id, status, currency, amount_cents FROM payments
		WHERE stripe_checkout_session_id = $1 FOR UPDATE`, sessionID).Scan(&p.id, &p.orderID, &p.status, &p.currency, &p.amountCents)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("payment: load payment: %w", err)
	}
	return p, err
}

func setPaymentStatus(ctx context.Context, tx pgx.Tx, id, status, paymentIntentID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE payments SET status = $2, stripe_payment_intent_id = COALESCE(NULLIF($3, ''), stripe_payment_intent_id)
		WHERE id = $1`, id, status, paymentIntentID)
	if err != nil {
		return fmt.Errorf("payment: set status: %w", err)
	}
	return nil
}

// succeed records a confirmed payment and marks the order paid, committing
// its reserved stock.
func (s *Service) succeed(ctx context.Context, tx pgx.Tx, p paymentRow, sess *stripe.CheckoutSession) error {
	log := logging.FromContext(ctx)
	if p.status == "succeeded" {
		return nil
	}
	if sess.AmountTotal != p.amountCents || !strings.EqualFold(string(sess.Currency), p.currency) {
		// Never fulfil an order for an amount other than its total.
		log.ErrorContext(ctx, "payment.amount_mismatch", "order_id", p.orderID,
			"expected_cents", p.amountCents, "paid_cents", sess.AmountTotal,
			"expected_currency", p.currency, "paid_currency", string(sess.Currency))
		return setPaymentStatus(ctx, tx, p.id, "failed", "")
	}

	var intentID string
	if sess.PaymentIntent != nil {
		intentID = sess.PaymentIntent.ID
	}

	// A customer could pay an old, retired session and a newer one. Only one
	// payment per order can succeed; the other must be refunded by hand.
	var alreadyPaid bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM payments WHERE order_id = $1 AND status = 'succeeded' AND id <> $2)`,
		p.orderID, p.id).Scan(&alreadyPaid); err != nil {
		return fmt.Errorf("payment: check earlier payment: %w", err)
	}
	if alreadyPaid {
		log.ErrorContext(ctx, "payment.requires_refund", "order_id", p.orderID, "reason", "order already paid",
			"amount_cents", p.amountCents, "payment_intent_id", intentID)
		return setPaymentStatus(ctx, tx, p.id, "failed", intentID)
	}

	if err := setPaymentStatus(ctx, tx, p.id, "succeeded", intentID); err != nil {
		return err
	}

	o, err := order.Lock(ctx, tx, p.orderID)
	if err != nil {
		return err
	}
	if o.Status != order.StatusPendingPayment {
		// Paid after the order was closed. The money was taken, so it must
		// be refunded by hand; the order is not reopened.
		log.ErrorContext(ctx, "payment.requires_refund", "order_id", o.ID, "order_status", string(o.Status),
			"amount_cents", p.amountCents, "payment_intent_id", intentID)
		return nil
	}
	if err := order.Transition(ctx, tx, o, order.StatusPaid, "payment confirmed by Stripe"); err != nil {
		return err
	}
	log.InfoContext(ctx, "payment.succeeded", "order_id", o.ID, "amount_cents", p.amountCents, "payment_intent_id", intentID)
	log.InfoContext(ctx, "order.status_changed", "order_id", o.ID,
		"from", string(order.StatusPendingPayment), "to", string(order.StatusPaid), "reason", "payment confirmed by Stripe")
	return nil
}

// fail records a declined delayed payment and cancels the order, releasing
// its stock.
func (s *Service) fail(ctx context.Context, tx pgx.Tx, p paymentRow) error {
	log := logging.FromContext(ctx)
	if err := setPaymentStatus(ctx, tx, p.id, "failed", ""); err != nil {
		return err
	}
	log.InfoContext(ctx, "payment.failed", "order_id", p.orderID)

	o, err := order.Lock(ctx, tx, p.orderID)
	if err != nil {
		return err
	}
	if o.Status != order.StatusPendingPayment {
		return nil
	}
	if err := order.Transition(ctx, tx, o, order.StatusCancelled, "payment failed"); err != nil {
		return err
	}
	log.InfoContext(ctx, "order.status_changed", "order_id", o.ID,
		"from", string(order.StatusPendingPayment), "to", string(order.StatusCancelled), "reason", "payment failed")
	return nil
}

// expire records an abandoned checkout. The order stays pending so the
// customer can start a new checkout until its reservation runs out.
func expire(ctx context.Context, tx pgx.Tx, p paymentRow) error {
	if p.status != "pending" {
		return nil
	}
	if err := setPaymentStatus(ctx, tx, p.id, "expired", ""); err != nil {
		return err
	}
	logging.FromContext(ctx).InfoContext(ctx, "payment.expired", "order_id", p.orderID)
	return nil
}
