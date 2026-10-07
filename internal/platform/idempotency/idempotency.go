// Package idempotency makes POST endpoints safe to retry. A client sends an
// Idempotency-Key header; the first request with that key runs, and any
// retry receives the stored response instead of running again.
package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

const (
	header  = "Idempotency-Key"
	maxKey  = 255
	maxBody = 1 << 20
)

// Store keeps request outcomes in Redis for TTL.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// New returns a Store.
func New(rdb *redis.Client, ttl time.Duration) *Store { return &Store{rdb: rdb, ttl: ttl} }

type record struct {
	Done        bool   `json:"done"`
	Fingerprint string `json:"fingerprint"`
	Status      int    `json:"status,omitempty"`
	Location    string `json:"location,omitempty"`
	Body        []byte `json:"body,omitempty"`
}

// Middleware applies idempotency to the routes it wraps. scope partitions
// keys (normally by user), so one user's key can never replay another's
// response.
//
// Like Stripe's API, it stores every result below 500, including client
// errors: a retry gets the same answer. Server errors are not stored, so the
// client can retry with the same key. A key reused with a different request
// is rejected. If Redis is unavailable the request runs without protection
// (logged); order creation is also guarded by the cart lock.
func (s *Store) Middleware(scope func(*http.Request) string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(header)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > maxKey {
				httpx.WriteError(w, r, httpx.NewError(http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY",
					"Idempotency-Key must be at most 255 characters"))
				return
			}

			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			if err != nil {
				httpx.WriteError(w, r, httpx.NewError(http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body is too large"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
			fingerprint := hex.EncodeToString(sum[:])

			ctx := r.Context()
			log := logging.FromContext(ctx)
			redisKey := "idempotency:" + scope(r) + ":" + key

			pending, _ := json.Marshal(record{Fingerprint: fingerprint})
			first, err := s.rdb.SetNX(ctx, redisKey, pending, s.ttl).Result()
			if err != nil {
				log.WarnContext(ctx, "idempotency.unavailable", "error", err)
				next.ServeHTTP(w, r)
				return
			}

			if !first {
				s.replay(w, r, redisKey, fingerprint)
				return
			}

			buf := httpx.NewBufferedResponse()
			next.ServeHTTP(buf, r)
			if buf.Status >= http.StatusInternalServerError {
				_ = s.rdb.Del(ctx, redisKey).Err()
			} else {
				done, _ := json.Marshal(record{
					Done: true, Fingerprint: fingerprint, Status: buf.Status,
					Location: buf.Header().Get("Location"), Body: buf.Body.Bytes(),
				})
				if err := s.rdb.Set(ctx, redisKey, done, s.ttl).Err(); err != nil {
					log.ErrorContext(ctx, "idempotency.store_failed", "error", err)
				}
			}
			buf.CopyTo(w)
		})
	}
}

func (s *Store) replay(w http.ResponseWriter, r *http.Request, redisKey, fingerprint string) {
	ctx := r.Context()
	raw, err := s.rdb.Get(ctx, redisKey).Bytes()
	var rec record
	if err == nil {
		err = json.Unmarshal(raw, &rec)
	}
	switch {
	case errors.Is(err, redis.Nil):
		// Expired between SETNX and GET: extremely unlikely; ask for a retry.
		fallthrough
	case err == nil && !rec.Done:
		httpx.WriteError(w, r, httpx.NewError(http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS",
			"A request with this Idempotency-Key is still being processed"))
		return
	case err != nil:
		httpx.WriteError(w, r, err)
		return
	case rec.Fingerprint != fingerprint:
		httpx.WriteError(w, r, httpx.NewError(http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
			"This Idempotency-Key was already used for a different request"))
		return
	}

	logging.FromContext(ctx).InfoContext(ctx, "idempotency.replayed", "status", rec.Status)
	w.Header().Set("Idempotent-Replayed", "true")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if rec.Location != "" {
		w.Header().Set("Location", rec.Location)
	}
	w.WriteHeader(rec.Status)
	_, _ = w.Write(rec.Body)
}
