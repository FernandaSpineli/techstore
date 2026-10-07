package app_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86/webhook"
)

// TestLogsAreStructuredAndSafe drives a whole purchase and audits every log
// line it produced: each must be JSON with an event name, the business
// events must be there, and no credential or personal data may appear.
func TestLogsAreStructuredAndSafe(t *testing.T) {
	ta := newTestApp(t)
	c := ta.seedCatalog()

	const password = "Sup3r-Secret-Pa55"
	reg := expect[authResponse](t, ta.do(http.MethodPost, "/api/v1/auth/register",
		map[string]string{"email": "carla@example.com", "password": password, "name": "Carla Souza"}, ""), http.StatusCreated)
	token := reg.Tokens.AccessToken
	ta.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "carla@example.com", "password": "wrong-" + password}, "")
	ta.do(http.MethodPost, "/api/v1/auth/password/forgot", map[string]string{"email": "carla@example.com"}, "")
	ta.do(http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": reg.Tokens.RefreshToken}, "")

	expect[cartView](t, ta.addToCart(token, c.iphone128.ID, 1), http.StatusOK)
	o := ta.placeOrder(token)
	co := expect[checkoutView](t, ta.checkout(token, o.ID), http.StatusCreated)

	payload, _ := json.Marshal(map[string]any{
		"id": "evt_audit", "object": "event", "type": "checkout.session.completed",
		"data": map[string]any{"object": session(co.SessionID, o.TotalCents, "paid")},
	})
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: testWebhookSecret, Timestamp: time.Now()})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhook", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", signed.Header)
	ta.h.ServeHTTP(httptest.NewRecorder(), req)

	expect[orderView](t, ta.do(http.MethodPatch, "/api/v1/admin/orders/"+o.ID+"/status", map[string]string{"status": "shipped"}, c.admin), http.StatusOK)
	ta.do(http.MethodGet, "/api/v1/orders/"+o.ID, nil, "forged")
	ta.app.Wait()

	logs := ta.logs.String()
	events := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %d is not JSON: %s", i+1, line)
		}
		event, _ := entry["event"].(string)
		if event == "" || entry["level"] == nil || entry["time"] == nil {
			t.Fatalf("log line %d lacks event/level/time: %s", i+1, line)
		}
		events[event] = true
	}

	for _, want := range []string{
		"http.request", "auth.user.registered", "auth.login.failed", "auth.password_reset.requested",
		"mail.sent", "cart.item.added", "order.created", "payment.checkout_created",
		"webhook.received", "payment.succeeded", "order.status_changed", "auth.unauthorized",
	} {
		if !events[want] {
			t.Errorf("no %q event logged", want)
		}
	}

	for name, secret := range map[string]string{
		"password":             password,
		"access token":         token,
		"refresh token":        reg.Tokens.RefreshToken,
		"stripe signature":     signed.Header,
		"webhook secret":       testWebhookSecret,
		"customer email":       "carla@example.com",
		"customer name":        "Carla Souza",
		"checkout URL (token)": co.URL,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("%s found in logs", name)
		}
	}
}
