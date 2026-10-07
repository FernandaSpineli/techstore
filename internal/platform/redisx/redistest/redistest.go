// Package redistest gives integration tests a real Redis. One container is
// started per test binary; New flushes it, so tests in a package must not
// run in parallel with each other.
package redistest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// Image is the Redis image used in tests; keep it in sync with
// docker-compose.yml.
const Image = "redis:8-alpine"

var (
	once     sync.Once
	url      string
	setupErr error
)

// New returns a client to an empty Redis database. It skips the test in
// -short mode.
func New(t testing.TB) *redis.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (skipped with -short)")
	}
	once.Do(func() { setupErr = setup() })
	if setupErr != nil {
		t.Fatalf("redistest: setup: %v", setupErr)
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	if err := client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("redistest: flush: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func setup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctr, err := tcredis.Run(ctx, Image)
	if err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	url, err = ctr.ConnectionString(ctx)
	return err
}
