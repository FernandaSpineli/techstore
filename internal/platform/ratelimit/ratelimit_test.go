package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/FernandaSpineli/techstore/internal/platform/redisx/redistest"
)

func TestAllowCountsPerKeyAndWindow(t *testing.T) {
	ctx := context.Background()
	l := New(redistest.New(t), "test", 3, time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 10, 0, time.UTC)
	l.now = func() time.Time { return now }

	for i := 1; i <= 4; i++ {
		res, err := l.Allow(ctx, "1.2.3.4")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allowed != (i <= 3) {
			t.Fatalf("request %d allowed = %v", i, res.Allowed)
		}
		if i == 4 && res.RetryAfter != 50*time.Second {
			t.Errorf("retry after = %v, want 50s", res.RetryAfter)
		}
	}
	if res, _ := l.Allow(ctx, "5.6.7.8"); !res.Allowed {
		t.Error("another client was limited")
	}

	now = now.Add(time.Minute) // next window
	if res, _ := l.Allow(ctx, "1.2.3.4"); !res.Allowed || res.Remaining != 2 {
		t.Errorf("new window = %+v, want allowed with 2 remaining", res)
	}
}

func TestMiddleware(t *testing.T) {
	l := New(redistest.New(t), "test", 2, time.Minute)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	var codes []int
	var last *httptest.ResponseRecorder
	for range 3 {
		last = httptest.NewRecorder()
		h.ServeHTTP(last, httptest.NewRequest(http.MethodPost, "/login", nil))
		codes = append(codes, last.Code)
	}
	if codes[0] != 204 || codes[1] != 204 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v", codes)
	}
	if last.Header().Get("Retry-After") == "" || last.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("headers = %v", last.Header())
	}
}

func TestMiddlewareFailsOpen(t *testing.T) {
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	h := New(dead, "test", 1, time.Minute).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want the request to pass when Redis is down", rec.Code)
	}
}
