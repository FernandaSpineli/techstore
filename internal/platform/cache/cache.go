// Package cache serves read-heavy, public GET endpoints from Redis.
//
// Cached entries live under a namespace version. Any write to the namespace
// bumps the version (one INCR), which makes every older entry unreachable at
// once; they then age out through their TTL. This avoids tracking which keys
// a write affects, at the cost of invalidating more than strictly needed,
// which is fine for a catalog that is read far more often than written.
package cache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// ResponseCache caches successful JSON responses for one namespace.
type ResponseCache struct {
	rdb       *redis.Client
	namespace string
	ttl       time.Duration
}

// New returns a ResponseCache.
func New(rdb *redis.Client, namespace string, ttl time.Duration) *ResponseCache {
	return &ResponseCache{rdb: rdb, namespace: namespace, ttl: ttl}
}

func (c *ResponseCache) versionKey() string { return "cache:" + c.namespace + ":version" }

func (c *ResponseCache) key(ctx context.Context, r *http.Request) (string, error) {
	v, err := c.rdb.Get(ctx, c.versionKey()).Result()
	if errors.Is(err, redis.Nil) {
		v = "0"
	} else if err != nil {
		return "", err
	}
	// Query().Encode() sorts parameters, so equivalent URLs share an entry.
	return fmt.Sprintf("cache:%s:v%s:%s?%s", c.namespace, v, r.URL.Path, r.URL.Query().Encode()), nil
}

// Middleware serves cached 200 responses and stores fresh ones. Only use it
// on routes whose response is the same for every caller. On any Redis error
// the request is served normally.
func (c *ResponseCache) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		log := logging.FromContext(ctx)
		key, err := c.key(ctx, r)
		if err != nil {
			log.WarnContext(ctx, "cache.unavailable", "namespace", c.namespace, "error", err)
			next.ServeHTTP(w, r)
			return
		}

		body, err := c.rdb.Get(ctx, key).Bytes()
		switch {
		case err == nil:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Cache", "HIT")
			_, _ = w.Write(body)
			return
		case !errors.Is(err, redis.Nil):
			log.WarnContext(ctx, "cache.unavailable", "namespace", c.namespace, "error", err)
			next.ServeHTTP(w, r)
			return
		}

		buf := httpx.NewBufferedResponse()
		next.ServeHTTP(buf, r)
		if buf.Status == http.StatusOK {
			if err := c.rdb.Set(ctx, key, buf.Body.Bytes(), c.ttl).Err(); err != nil {
				log.WarnContext(ctx, "cache.store_failed", "namespace", c.namespace, "error", err)
			}
		}
		buf.Header().Set("X-Cache", "MISS")
		buf.CopyTo(w)
	})
}

// Invalidate makes every cached entry of the namespace stale.
func (c *ResponseCache) Invalidate(ctx context.Context) error {
	return c.rdb.Incr(ctx, c.versionKey()).Err()
}

// InvalidateOnWrite invalidates the namespace after every successful
// (2xx) request it wraps.
func (c *ResponseCache) InvalidateOnWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := httpx.NewBufferedResponse()
		next.ServeHTTP(buf, r)
		if buf.Status >= 200 && buf.Status < 300 {
			ctx := r.Context()
			if err := c.Invalidate(ctx); err != nil {
				// Entries still expire through their TTL.
				logging.FromContext(ctx).ErrorContext(ctx, "cache.invalidate_failed", "namespace", c.namespace, "error", err)
			}
		}
		buf.CopyTo(w)
	})
}
