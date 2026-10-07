package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FernandaSpineli/techstore/internal/platform/redisx/redistest"
)

func TestResponseCache(t *testing.T) {
	c := New(redistest.New(t), "catalog", time.Minute)
	calls := 0
	read := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("fail") != "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"n":1}`))
	}))
	write := c.InvalidateOnWrite(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		read.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		return rec
	}

	if rec := get("/products?b=2&a=1"); rec.Header().Get("X-Cache") != "MISS" || rec.Body.String() != `{"n":1}` {
		t.Fatalf("first read: %v %q", rec.Header(), rec.Body)
	}
	// Same parameters in another order hit the same entry.
	if rec := get("/products?a=1&b=2"); rec.Header().Get("X-Cache") != "HIT" || rec.Body.String() != `{"n":1}` || calls != 1 {
		t.Fatalf("second read: %v, %d handler calls", rec.Header(), calls)
	}

	write.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/products", nil))
	if rec := get("/products?a=1&b=2"); rec.Header().Get("X-Cache") != "MISS" || calls != 2 {
		t.Fatalf("read after write: %v, %d handler calls", rec.Header(), calls)
	}

	// Errors are never cached.
	get("/products?fail=1")
	get("/products?fail=1")
	if calls != 4 {
		t.Errorf("handler calls = %d, want error responses to bypass the cache", calls)
	}
}
