package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/FernandaSpineli/techstore/internal/app"
	"github.com/FernandaSpineli/techstore/internal/payment"
	"github.com/FernandaSpineli/techstore/internal/platform/config"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
	"github.com/FernandaSpineli/techstore/internal/platform/mail"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres/pgtest"
)

// fakeGateway stands in for Stripe. Like Stripe, it returns the original
// session when an idempotency key is reused.
type fakeGateway struct {
	mu       sync.Mutex
	requests []payment.CheckoutRequest
	byKey    map[string]payment.Session
	err      error
}

func (g *fakeGateway) CreateCheckoutSession(_ context.Context, req payment.CheckoutRequest) (payment.Session, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, req)
	if g.err != nil {
		return payment.Session{}, g.err
	}
	if s, ok := g.byKey[req.IdempotencyKey]; ok {
		return s, nil
	}
	if g.byKey == nil {
		g.byKey = map[string]payment.Session{}
	}
	id := fmt.Sprintf("cs_test_%d", len(g.byKey)+1)
	s := payment.Session{ID: id, URL: "https://checkout.stripe.test/c/pay/" + id, ExpiresAt: req.ExpiresAt}
	g.byKey[req.IdempotencyKey] = s
	return s, nil
}

func (g *fakeGateway) calls() []payment.CheckoutRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]payment.CheckoutRequest(nil), g.requests...)
}

// testApp is the full application running against a fresh database.
type testApp struct {
	t       *testing.T
	h       http.Handler
	app     *app.App
	db      *pgxpool.Pool
	mailer  *fakeMailer
	gateway *fakeGateway
	logs    *syncBuffer
}

const testWebhookSecret = "whsec_test_secret"

type testOption func(*app.Deps)

// withoutPayments runs the app as if Stripe were not configured.
func withoutPayments(d *app.Deps) { d.PaymentGateway = nil }

func newTestApp(t *testing.T, opts ...testOption) *testApp {
	t.Helper()
	ta := &testApp{t: t, db: pgtest.New(t), mailer: &fakeMailer{}, gateway: &fakeGateway{}, logs: &syncBuffer{}}

	deps := app.Deps{
		Config: config.Config{
			Env:                 config.EnvTest,
			BaseURL:             "http://shop.test",
			JWTSecret:           "test-secret-test-secret-test-secret",
			AccessTokenTTL:      15 * time.Minute,
			RefreshTokenTTL:     time.Hour,
			PasswordResetTTL:    30 * time.Minute,
			OrderReservationTTL: 30 * time.Minute,
			StripeWebhookSecret: testWebhookSecret,
		},
		Logger:         logging.New(ta.logs, slog.LevelDebug),
		DB:             ta.db,
		Mailer:         ta.mailer,
		PaymentGateway: ta.gateway,
		BcryptCost:     bcrypt.MinCost,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	a, err := app.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Wait)
	ta.app, ta.h = a, a.Handler
	return ta
}

// do sends a request with an optional JSON body and bearer token.
func (ta *testApp) do(method, path string, body any, token string) *httptest.ResponseRecorder {
	ta.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			ta.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	ta.h.ServeHTTP(rec, req)
	return rec
}

// expect fails the test unless rec has the given status, and returns the
// decoded JSON body.
func expect[T any](t *testing.T, rec *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body)
	}
	var v T
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("decode body %s: %v", rec.Body, err)
		}
	}
	return v
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		// Details is a list of field problems for VALIDATION_FAILED and an
		// error-specific object otherwise.
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

type fieldProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// problems decodes the field problems of a VALIDATION_FAILED error.
func (e apiError) problems(t *testing.T) []fieldProblem {
	t.Helper()
	var ps []fieldProblem
	if err := json.Unmarshal(e.Error.Details, &ps); err != nil {
		t.Fatalf("details %s are not field problems: %v", e.Error.Details, err)
	}
	return ps
}

// expectError asserts an error response with the given status and code.
func expectError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) apiError {
	t.Helper()
	e := expect[apiError](t, rec, status)
	if e.Error.Code != code {
		t.Fatalf("error code = %q, want %q; body: %s", e.Error.Code, code, rec.Body)
	}
	return e
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (m *fakeMailer) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *fakeMailer) messages() []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mail.Message(nil), m.sent...)
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of background
// goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
