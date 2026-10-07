// Package redisx opens the Redis client. It is named redisx to avoid
// clashing with the go-redis package it wraps.
package redisx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Connect creates a Redis client and verifies the server is reachable.
func Connect(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("redis: invalid REDIS_URL")
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return client, nil
}

// SetLogger routes go-redis internal messages (reconnects, pool errors)
// through slog so every log line stays structured JSON.
func SetLogger(l *slog.Logger) {
	redis.SetLogger(slogAdapter{l})
}

type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) Printf(ctx context.Context, format string, v ...any) {
	a.l.WarnContext(ctx, "redis.client", "detail", fmt.Sprintf(format, v...))
}
