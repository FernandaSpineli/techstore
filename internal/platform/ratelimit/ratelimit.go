// Package ratelimit limits requests per client with fixed-window counters
// in Redis, so the limit holds across every API instance.
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// Limiter allows Limit requests per Window for each key.
//
// A fixed window is the simplest atomic scheme (one INCR per request); its
// known weakness is a burst of up to 2x the limit across a window boundary,
// which is acceptable for brute-force protection.
type Limiter struct {
	rdb    *redis.Client
	name   string
	limit  int
	window time.Duration
	now    func() time.Time
}

// New returns a Limiter. name namespaces its keys (e.g. "auth").
func New(rdb *redis.Client, name string, limit int, window time.Duration) *Limiter {
	return &Limiter{rdb: rdb, name: name, limit: limit, window: window, now: time.Now}
}

// Result is the outcome of one Allow call.
type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

// Allow counts a request for key and reports whether it is within the limit.
func (l *Limiter) Allow(ctx context.Context, key string) (Result, error) {
	now := l.now()
	start := now.Truncate(l.window)
	redisKey := fmt.Sprintf("ratelimit:%s:%s:%d", l.name, key, start.Unix())

	var incr *redis.IntCmd
	_, err := l.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		incr = p.Incr(ctx, redisKey)
		p.ExpireNX(ctx, redisKey, l.window)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: %w", err)
	}
	count := int(incr.Val())
	return Result{
		Allowed:    count <= l.limit,
		Remaining:  max(l.limit-count, 0),
		RetryAfter: start.Add(l.window).Sub(now),
	}, nil
}

// Middleware rejects requests over the limit with 429, keyed by client IP.
// If Redis is unavailable it lets requests through: an outage of the limiter
// should not become an outage of the shop.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		res, err := l.Allow(ctx, httpx.ClientIP(r))
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "ratelimit.unavailable", "limiter", l.name, "error", err)
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
		if !res.Allowed {
			retry := int(math.Ceil(res.RetryAfter.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			logging.FromContext(ctx).WarnContext(ctx, "ratelimit.exceeded",
				slog.String("limiter", l.name), slog.String("route", r.Pattern), slog.String("remote_ip", httpx.ClientIP(r)))
			httpx.WriteError(w, r, httpx.NewError(http.StatusTooManyRequests, "RATE_LIMITED",
				"Too many requests; retry after "+strconv.Itoa(retry)+" seconds"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
