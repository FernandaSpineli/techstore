package idempotency

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FernandaSpineli/techstore/internal/platform/redisx/redistest"
)

func setup(t *testing.T, status int) (http.Handler, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	h := New(redistest.New(t), time.Hour).Middleware(func(r *http.Request) string { return r.Header.Get("X-User") })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := calls.Add(1)
			time.Sleep(20 * time.Millisecond)
			w.Header().Set("Location", fmt.Sprintf("/orders/%d", n))
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"order":%d}`, n)
		}))
	return h, &calls
}

func send(h http.Handler, user, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
	r.Header.Set("X-User", user)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestReplay(t *testing.T) {
	h, calls := setup(t, http.StatusCreated)

	first := send(h, "u1", "k1", "")
	again := send(h, "u1", "k1", "")
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", calls.Load())
	}
	if again.Code != http.StatusCreated || again.Body.String() != first.Body.String() ||
		again.Header().Get("Location") != "/orders/1" || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay = %d %v %q", again.Code, again.Header(), again.Body)
	}

	// Without a key, or with another user's key space, the request runs.
	send(h, "u1", "", "")
	send(h, "u2", "k1", "")
	if calls.Load() != 3 {
		t.Errorf("handler ran %d times, want 3", calls.Load())
	}
}

func TestKeyReusedWithDifferentRequest(t *testing.T) {
	h, _ := setup(t, http.StatusCreated)
	send(h, "u1", "k1", `{"a":1}`)
	if rec := send(h, "u1", "k1", `{"a":2}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Errorf("reuse = %d %s", rec.Code, rec.Body)
	}
}

func TestConcurrentRequestsRunOnce(t *testing.T) {
	h, calls := setup(t, http.StatusCreated)
	var wg sync.WaitGroup
	var created, inProgress atomic.Int32
	for range 10 {
		wg.Go(func() {
			switch send(h, "u1", "k1", "").Code {
			case http.StatusCreated:
				created.Add(1)
			case http.StatusConflict:
				inProgress.Add(1)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 || created.Load()+inProgress.Load() != 10 {
		t.Errorf("handler ran %d times; %d created, %d in progress", calls.Load(), created.Load(), inProgress.Load())
	}
}

func TestServerErrorsAreNotStored(t *testing.T) {
	h, calls := setup(t, http.StatusInternalServerError)
	send(h, "u1", "k1", "")
	send(h, "u1", "k1", "")
	if calls.Load() != 2 {
		t.Errorf("handler ran %d times, want a 500 to be retryable", calls.Load())
	}
}

func TestKeyTooLong(t *testing.T) {
	h, calls := setup(t, http.StatusCreated)
	if rec := send(h, "u1", strings.Repeat("k", 256), ""); rec.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Errorf("long key = %d, %d calls", rec.Code, calls.Load())
	}
}
