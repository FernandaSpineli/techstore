package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86/webhook"
)

type checkoutView struct {
	SessionID string    `json:"session_id"`
	URL       string    `json:"checkout_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// pendingOrder places an order for 2 x IP15-128 (R$ 9.998,00).
func (ta *testApp) pendingOrder(c seededCatalog) (user string, o orderView) {
	ta.t.Helper()
	user = ta.signIn("customer")
	expect[cartView](ta.t, ta.addToCart(user, c.iphone128.ID, 2), http.StatusOK)
	return user, ta.placeOrder(user)
}

func (ta *testApp) checkout(user, orderID string) *httptest.ResponseRecorder {
	return ta.do(http.MethodPost, "/api/v1/orders/"+orderID+"/checkout", nil, user)
}

// webhookAt sends a Stripe event signed with secret at the given time.
func (ta *testApp) webhookAt(secret string, at time.Time, eventID, eventType string, session map[string]any) *httptest.ResponseRecorder {
	ta.t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id": eventID, "object": "event", "type": eventType, "api_version": "2024-06-20",
		"created": at.Unix(), "data": map[string]any{"object": session},
	})
	if err != nil {
		ta.t.Fatal(err)
	}
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret, Timestamp: at})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhook", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	rec := httptest.NewRecorder()
	ta.h.ServeHTTP(rec, req)
	return rec
}

func (ta *testApp) webhook(eventID, eventType string, session map[string]any) *httptest.ResponseRecorder {
	return ta.webhookAt(testWebhookSecret, time.Now(), eventID, eventType, session)
}

func session(id string, amount int64, paymentStatus string) map[string]any {
	return map[string]any{
		"id": id, "object": "checkout.session", "amount_total": amount, "currency": "brl",
		"payment_status": paymentStatus, "payment_intent": "pi_test_123",
	}
}

func (ta *testApp) paymentStatus(sessionID string) string {
	ta.t.Helper()
	var s string
	if err := ta.db.QueryRow(context.Background(), `SELECT status FROM payments WHERE stripe_checkout_session_id = $1`, sessionID).Scan(&s); err != nil {
		ta.t.Fatal(err)
	}
	return s
}

func (ta *testApp) order(user, id string) orderView {
	ta.t.Helper()
	return expect[orderView](ta.t, ta.do(http.MethodGet, "/api/v1/orders/"+id, nil, user), http.StatusOK)
}

func TestCheckoutCreatesSessionFromTheOrder(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)

	co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)
	if co.SessionID != "cs_test_1" || !strings.HasPrefix(co.URL, "https://checkout.stripe.test/") {
		t.Errorf("checkout = %+v", co)
	}

	calls := ta.gateway.calls()
	if len(calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(calls))
	}
	req := calls[0]
	if req.OrderID != o.ID || req.Currency != "brl" || len(req.Lines) != 1 || req.Lines[0].UnitPriceCents != 499900 || req.Lines[0].Quantity != 2 {
		t.Errorf("checkout request = %+v", req)
	}
	if req.IdempotencyKey != "checkout-"+o.ID+"-1" || !strings.HasPrefix(req.CustomerEmail, "customer") {
		t.Errorf("idempotency key / email = %q / %q", req.IdempotencyKey, req.CustomerEmail)
	}
	if !strings.HasPrefix(req.SuccessURL, "http://shop.test/checkout/success?order_id=") {
		t.Errorf("success URL = %q", req.SuccessURL)
	}
	// Stripe rejects sessions expiring less than 30 minutes after creation.
	if ttl := time.Until(req.ExpiresAt); ttl <= 30*time.Minute || ttl > 32*time.Minute {
		t.Errorf("session expires in %v, want just over Stripe's 30-minute minimum", ttl)
	}

	// The reservation now outlives the session, so the sweeper cannot expire
	// an order that may still be paid.
	got := ta.order(user, o.ID)
	if !got.ExpiresAt.After(co.ExpiresAt) {
		t.Errorf("order expires at %v, session at %v; want the order to outlive the session", got.ExpiresAt, co.ExpiresAt)
	}
	if ta.paymentStatus(co.SessionID) != "pending" {
		t.Error("payment not recorded as pending")
	}
}

func TestCheckoutReusesTheOpenSession(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)

	first := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)
	second := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusOK)
	if first.SessionID != second.SessionID || len(ta.gateway.calls()) != 1 {
		t.Errorf("second checkout = %+v after %d gateway calls; want the same session and 1 call", second, len(ta.gateway.calls()))
	}
}

func TestConcurrentCheckoutsShareOneSession(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)

	var wg sync.WaitGroup
	var mu sync.Mutex
	sessions := map[string]bool{}
	for range 8 {
		wg.Go(func() {
			rec := ta.checkout(user, o.ID)
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Errorf("checkout status %d: %s", rec.Code, rec.Body)
				return
			}
			var co checkoutView
			_ = json.Unmarshal(rec.Body.Bytes(), &co)
			mu.Lock()
			sessions[co.SessionID] = true
			mu.Unlock()
		})
	}
	wg.Wait()
	var payments int
	if err := ta.db.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE order_id = $1`, o.ID).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || payments != 1 {
		t.Errorf("%d distinct sessions and %d payment rows, want 1 and 1", len(sessions), payments)
	}
}

func TestCheckoutRejections(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	other := ta.signIn("customer")

	expectError(t, ta.checkout("", o.ID), http.StatusUnauthorized, "UNAUTHORIZED")
	expectError(t, ta.checkout(other, o.ID), http.StatusNotFound, "ORDER_NOT_FOUND")
	expectError(t, ta.checkout(user, "nope"), http.StatusNotFound, "ORDER_NOT_FOUND")

	expect[orderView](t, ta.do(http.MethodPost, "/api/v1/orders/"+o.ID+"/cancel", nil, user), http.StatusOK)
	expectError(t, ta.checkout(user, o.ID), http.StatusConflict, "ORDER_NOT_PAYABLE")
	if len(ta.gateway.calls()) != 0 {
		t.Error("gateway was called for a rejected checkout")
	}
}

func TestCheckoutWhenStripeFails(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	ta.gateway.err = errors.New("stripe: connection reset (request_id=req_123)")

	rec := ta.checkout(user, o.ID)
	expectError(t, rec, http.StatusBadGateway, "PAYMENT_PROVIDER_ERROR")
	if strings.Contains(rec.Body.String(), "req_123") {
		t.Error("provider error details leaked to the client")
	}
	if !strings.Contains(ta.logs.String(), `"event":"stripe.request_failed"`) {
		t.Error("provider failure not logged")
	}

	// Once Stripe recovers, the same order can be paid.
	ta.gateway.err = nil
	expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)
}

func TestCheckoutWithoutStripeConfigured(t *testing.T) {
	ta := newTestApp(t, withoutPayments)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)

	expectError(t, ta.checkout(user, o.ID), http.StatusServiceUnavailable, "PAYMENTS_UNAVAILABLE")
	expectError(t, ta.webhook("evt_1", "checkout.session.completed", session("cs_x", 1, "paid")),
		http.StatusServiceUnavailable, "PAYMENTS_UNAVAILABLE")
}

func TestWebhookConfirmsPayment(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)

	expect[map[string]bool](t, ta.webhook("evt_paid", "checkout.session.completed", session(co.SessionID, o.TotalCents, "paid")), http.StatusOK)

	got := ta.order(user, o.ID)
	last := got.StatusHistory[len(got.StatusHistory)-1]
	if got.Status != "paid" || got.ExpiresAt != nil || last.Reason != "payment confirmed by Stripe" {
		t.Errorf("order = %s (expires %v), last change %+v", got.Status, got.ExpiresAt, last)
	}
	// The reservation became a sale.
	if s := ta.stock(c.admin, c.iphone128.ID); s != (stockLevel{OnHand: 3, Reserved: 0, Available: 3}) {
		t.Errorf("stock = %+v, want 2 units sold", s)
	}
	if ta.paymentStatus(co.SessionID) != "succeeded" {
		t.Error("payment not marked succeeded")
	}
	var intent string
	_ = ta.db.QueryRow(context.Background(), `SELECT stripe_payment_intent_id FROM payments WHERE stripe_checkout_session_id = $1`, co.SessionID).Scan(&intent)
	if intent != "pi_test_123" {
		t.Errorf("payment intent = %q", intent)
	}
}

func TestDuplicateWebhooksAreAppliedOnce(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)
	sess := session(co.SessionID, o.TotalCents, "paid")

	// Stripe delivers at least once; here the same event arrives five times
	// at the same moment, then once more later.
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if rec := ta.webhook("evt_dup", "checkout.session.completed", sess); rec.Code != http.StatusOK {
				t.Errorf("delivery status %d: %s", rec.Code, rec.Body)
			}
		})
	}
	wg.Wait()
	expect[map[string]bool](t, ta.webhook("evt_dup", "checkout.session.completed", sess), http.StatusOK)

	got := ta.order(user, o.ID)
	if got.Status != "paid" || len(got.StatusHistory) != 2 {
		t.Errorf("order = %s with %d history entries, want paid with 2", got.Status, len(got.StatusHistory))
	}
	if s := ta.stock(c.admin, c.iphone128.ID); s.OnHand != 3 {
		t.Errorf("stock committed more than once: %+v", s)
	}
	if !strings.Contains(ta.logs.String(), `"event":"webhook.duplicate"`) {
		t.Error("duplicates not logged")
	}
}

func TestWebhookRejectsBadSignatures(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)
	sess := session(co.SessionID, o.TotalCents, "paid")

	expectError(t, ta.webhookAt("whsec_attacker", time.Now(), "evt_a", "checkout.session.completed", sess),
		http.StatusBadRequest, "INVALID_SIGNATURE")
	// A correctly signed but old event (a replay) is refused too.
	expectError(t, ta.webhookAt(testWebhookSecret, time.Now().Add(-10*time.Minute), "evt_b", "checkout.session.completed", sess),
		http.StatusBadRequest, "INVALID_SIGNATURE")

	unsigned := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhook", strings.NewReader(`{"id":"evt_c"}`))
	rec := httptest.NewRecorder()
	ta.h.ServeHTTP(rec, unsigned)
	expectError(t, rec, http.StatusBadRequest, "INVALID_SIGNATURE")

	if got := ta.order(user, o.ID); got.Status != "pending_payment" {
		t.Errorf("forged webhook changed the order to %s", got.Status)
	}
}

func TestDelayedPaymentMethods(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()

	t.Run("succeeds later", func(t *testing.T) {
		user, o := ta.pendingOrder(c)
		co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)

		// Boleto: the session completes before the money arrives.
		expect[map[string]bool](t, ta.webhook("evt_s1", "checkout.session.completed", session(co.SessionID, o.TotalCents, "unpaid")), http.StatusOK)
		if got := ta.order(user, o.ID); got.Status != "pending_payment" {
			t.Fatalf("order = %s before the payment cleared", got.Status)
		}
		expect[map[string]bool](t, ta.webhook("evt_s2", "checkout.session.async_payment_succeeded", session(co.SessionID, o.TotalCents, "paid")), http.StatusOK)
		if got := ta.order(user, o.ID); got.Status != "paid" {
			t.Fatalf("order = %s after async success", got.Status)
		}
	})

	t.Run("is declined", func(t *testing.T) {
		before := ta.stock(c.admin, c.iphone128.ID)
		user, o := ta.pendingOrder(c)
		co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)

		expect[map[string]bool](t, ta.webhook("evt_f1", "checkout.session.completed", session(co.SessionID, o.TotalCents, "unpaid")), http.StatusOK)
		expect[map[string]bool](t, ta.webhook("evt_f2", "checkout.session.async_payment_failed", session(co.SessionID, o.TotalCents, "unpaid")), http.StatusOK)

		got := ta.order(user, o.ID)
		if got.Status != "cancelled" || got.StatusHistory[len(got.StatusHistory)-1].Reason != "payment failed" {
			t.Errorf("order = %+v", got)
		}
		if ta.paymentStatus(co.SessionID) != "failed" {
			t.Error("payment not marked failed")
		}
		if after := ta.stock(c.admin, c.iphone128.ID); after != before {
			t.Errorf("stock = %+v, want it back to %+v", after, before)
		}
	})
}

func TestExpiredSessionAllowsANewCheckout(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	first := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)

	expect[map[string]bool](t, ta.webhook("evt_exp", "checkout.session.expired", session(first.SessionID, o.TotalCents, "unpaid")), http.StatusOK)
	if ta.paymentStatus(first.SessionID) != "expired" {
		t.Error("payment not marked expired")
	}
	if got := ta.order(user, o.ID); got.Status != "pending_payment" {
		t.Errorf("order = %s; an abandoned checkout must not close the order", got.Status)
	}

	second := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)
	calls := ta.gateway.calls()
	if second.SessionID == first.SessionID || calls[len(calls)-1].IdempotencyKey != "checkout-"+o.ID+"-2" {
		t.Errorf("second checkout = %+v, key %q", second, calls[len(calls)-1].IdempotencyKey)
	}
}

func TestWebhookWithWrongAmountDoesNotFulfil(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)

	expect[map[string]bool](t, ta.webhook("evt_amt", "checkout.session.completed", session(co.SessionID, 100, "paid")), http.StatusOK)

	if got := ta.order(user, o.ID); got.Status != "pending_payment" {
		t.Errorf("order = %s after an underpayment", got.Status)
	}
	if ta.paymentStatus(co.SessionID) != "failed" || !strings.Contains(ta.logs.String(), `"event":"payment.amount_mismatch"`) {
		t.Error("amount mismatch not recorded and logged")
	}
}

func TestPaymentForAClosedOrderIsFlaggedForRefund(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)

	// The order expired (e.g. the webhook was delayed for a long time).
	if _, err := ta.db.Exec(context.Background(), `UPDATE inventory SET reserved = reserved - 2 WHERE variant_id = $1`, c.iphone128.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.db.Exec(context.Background(), `UPDATE orders SET status = 'expired', expires_at = NULL WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}

	expect[map[string]bool](t, ta.webhook("evt_late", "checkout.session.completed", session(co.SessionID, o.TotalCents, "paid")), http.StatusOK)

	if got := ta.order(user, o.ID); got.Status != "expired" {
		t.Errorf("order = %s; a closed order must not be reopened", got.Status)
	}
	if !strings.Contains(ta.logs.String(), `"event":"payment.requires_refund"`) {
		t.Error("late payment not flagged for refund")
	}
}

func TestCancelIsBlockedWhileCheckoutIsOpen(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()
	user, o := ta.pendingOrder(c)
	co := expect[checkoutView](t, ta.checkout(user, o.ID), http.StatusCreated)

	expectError(t, ta.do(http.MethodPost, "/api/v1/orders/"+o.ID+"/cancel", nil, user), http.StatusConflict, "PAYMENT_IN_PROGRESS")
	expectError(t, ta.do(http.MethodPatch, "/api/v1/admin/orders/"+o.ID+"/status", map[string]string{"status": "cancelled"}, c.admin),
		http.StatusConflict, "PAYMENT_IN_PROGRESS")

	expect[map[string]bool](t, ta.webhook("evt_x", "checkout.session.expired", session(co.SessionID, o.TotalCents, "unpaid")), http.StatusOK)
	expect[orderView](t, ta.do(http.MethodPost, "/api/v1/orders/"+o.ID+"/cancel", nil, user), http.StatusOK)
}

func TestIrrelevantWebhooksAreAcknowledged(t *testing.T) {
	ta := newTestApp(t)
	expect[map[string]bool](t, ta.webhook("evt_other", "customer.created", map[string]any{"id": "cus_1", "object": "customer"}), http.StatusOK)
	expect[map[string]bool](t, ta.webhook("evt_unknown", "checkout.session.completed", session("cs_not_ours", 100, "paid")), http.StatusOK)
	if !strings.Contains(ta.logs.String(), `"event":"webhook.unknown_session"`) {
		t.Error("unknown session not logged")
	}
}
