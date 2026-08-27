package security

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// CounterStore records a counter for a fixed time window.
type CounterStore interface {
	Increment(ctx context.Context, key string, window time.Duration) (count int64, ttl time.Duration, err error)
}

type memoryCounterStore struct {
	now   func() time.Time
	mu    sync.Mutex
	items map[string]memoryBucket
}

type memoryBucket struct {
	started time.Time
	used    int64
}

func NewMemoryCounterStore(now func() time.Time) CounterStore {
	if now == nil {
		now = time.Now
	}
	return &memoryCounterStore{now: now, items: make(map[string]memoryBucket)}
}

func (s *memoryCounterStore) Increment(_ context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	if window <= 0 {
		return 0, 0, fmt.Errorf("counter window must be positive")
	}

	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	item := s.items[key]
	if item.started.IsZero() || now.Sub(item.started) >= window {
		item = memoryBucket{started: now}
	}
	item.used++
	s.items[key] = item
	ttl := window - now.Sub(item.started)
	if ttl < time.Second {
		ttl = time.Second
	}
	return item.used, ttl, nil
}

var fixedWindowScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return {current, redis.call('TTL', KEYS[1])}
`)

type RedisCounterStore struct {
	client      *redis.Client
	environment string
}

func NewRedisCounterStore(ctx context.Context, addr, environment string) (*RedisCounterStore, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &RedisCounterStore{client: client, environment: environment}, nil
}

func (s *RedisCounterStore) Increment(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	if window <= 0 {
		return 0, 0, fmt.Errorf("counter window must be positive")
	}
	seconds := int64(window / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	result, err := fixedWindowScript.Run(ctx, s.client, []string{key}, seconds).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("redis increment counter: %w", err)
	}
	values, ok := result.([]interface{})
	if !ok || len(values) != 2 {
		return 0, 0, fmt.Errorf("redis increment counter returned invalid result")
	}
	count, ok := values[0].(int64)
	if !ok {
		return 0, 0, fmt.Errorf("redis increment counter returned invalid count")
	}
	ttlSeconds, ok := values[1].(int64)
	if !ok || ttlSeconds < 0 {
		return 0, 0, fmt.Errorf("redis increment counter returned invalid ttl")
	}
	return count, time.Duration(ttlSeconds) * time.Second, nil
}

func (s *RedisCounterStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
