package payment

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStripe records the requests it receives and answers with the given
// statuses in turn (the last one repeats). Status 0 drops the connection,
// like a network failure.
type fakeStripe struct {
	mu       sync.Mutex
	statuses []int
	requests []*http.Request
	forms    []url.Values
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.forms = append(f.forms, form)
	status := f.statuses[min(len(f.requests), len(f.statuses))-1]
	f.mu.Unlock()

	if status == 0 {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_fake")
	w.WriteHeader(status)
	if status == http.StatusOK {
		_, _ = io.WriteString(w, `{"id":"cs_test_abc","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_test_abc","expires_at":1791378000}`)
		return
	}
	_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"parameter_invalid_integer","message":"Invalid integer: abc"}}`)
}

func newFakeStripe(t *testing.T, statuses ...int) (*fakeStripe, *StripeGateway, *bytes.Buffer) {
	t.Helper()
	f := &fakeStripe{statuses: statuses}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	return f, newStripeGateway("sk_test_fake", slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), srv.URL), &logs
}

var testRequest = CheckoutRequest{
	OrderID:        "order-1",
	CustomerEmail:  "ana@example.com",
	Currency:       "brl",
	Lines:          []CheckoutLine{{Name: "iPhone 15 — 128GB", UnitPriceCents: 499900, Quantity: 2}},
	SuccessURL:     "http://shop.test/checkout/success?order_id=order-1",
	CancelURL:      "http://shop.test/checkout/cancel?order_id=order-1",
	ExpiresAt:      time.Unix(1791378000, 0),
	IdempotencyKey: "checkout-order-1-1",
}

func TestStripeGatewaySendsTheExpectedRequest(t *testing.T) {
	f, g, _ := newFakeStripe(t, http.StatusOK)

	s, err := g.CreateCheckoutSession(context.Background(), testRequest)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "cs_test_abc" || s.URL != "https://checkout.stripe.com/c/pay/cs_test_abc" || s.ExpiresAt.Unix() != 1791378000 {
		t.Errorf("session = %+v", s)
	}

	r, form := f.requests[0], f.forms[0]
	if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
		t.Errorf("request = %s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer sk_test_fake" || r.Header.Get("Idempotency-Key") != "checkout-order-1-1" {
		t.Errorf("headers = %v", r.Header)
	}
	for key, want := range map[string]string{
		"mode":                                          "payment",
		"client_reference_id":                           "order-1",
		"customer_email":                                "ana@example.com",
		"expires_at":                                    "1791378000",
		"metadata[order_id]":                            "order-1",
		"line_items[0][quantity]":                       "2",
		"line_items[0][price_data][currency]":           "brl",
		"line_items[0][price_data][unit_amount]":        "499900",
		"line_items[0][price_data][product_data][name]": "iPhone 15 — 128GB",
	} {
		if got := form.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// A network failure is retried with the same idempotency key, so a retry
// can never create a second session.
func TestStripeGatewayRetriesWithTheSameIdempotencyKey(t *testing.T) {
	f, g, logs := newFakeStripe(t, 0, http.StatusOK)

	if _, err := g.CreateCheckoutSession(context.Background(), testRequest); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("%d requests, want 2 (one retry)", len(f.requests))
	}
	if a, b := f.requests[0].Header.Get("Idempotency-Key"), f.requests[1].Header.Get("Idempotency-Key"); a != b || a == "" {
		t.Errorf("idempotency keys = %q, %q; want the same key", a, b)
	}
	// The library's own messages are structured and never at error level.
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line != "" && !strings.Contains(line, `"level":"DEBUG"`) {
			t.Errorf("stripe client log not at debug level: %s", line)
		}
	}
}

func TestStripeGatewayDescribesErrors(t *testing.T) {
	_, g, _ := newFakeStripe(t, http.StatusBadRequest)

	_, err := g.CreateCheckoutSession(context.Background(), testRequest)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"invalid_request_error", "parameter_invalid_integer", "status=400", "req_fake"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "sk_test_fake") {
		t.Error("error contains the API key")
	}
}
