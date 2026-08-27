package security

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCounterStoreReturnsCountAndTTL(t *testing.T) {
	clock := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	store := NewMemoryCounterStore(func() time.Time { return clock })

	count, ttl, err := store.Increment(context.Background(), "key", time.Minute)
	if err != nil || count != 1 || ttl != time.Minute {
		t.Fatalf("got count=%d ttl=%v err=%v", count, ttl, err)
	}
}

func TestRedisCounterStoreFailsPingForUnavailableAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := NewRedisCounterStore(ctx, "127.0.0.1:1", "test")
	if err == nil {
		t.Fatal("NewRedisCounterStore() error = nil, want ping failure")
	}
}

func TestMemoryCounterStoreResetsAfterWindow(t *testing.T) {
	clock := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	store := NewMemoryCounterStore(func() time.Time { return clock })

	count, _, err := store.Increment(context.Background(), "key", time.Minute)
	if err != nil || count != 1 {
		t.Fatalf("first increment = count=%d err=%v, want count 1", count, err)
	}
	clock = clock.Add(time.Minute)
	count, _, err = store.Increment(context.Background(), "key", time.Minute)
	if err != nil || count != 1 {
		t.Fatalf("increment after window = count=%d err=%v, want count 1", count, err)
	}
}
