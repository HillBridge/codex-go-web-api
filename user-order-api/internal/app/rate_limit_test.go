package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/auth"
	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/security"
	"bridge-go/user-order-api/internal/user"
)

type appCounterStore struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (s *appCounterStore) Increment(_ context.Context, key string, _ time.Duration) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = make(map[string]int64)
	}
	s.counts[key]++
	return s.counts[key], time.Minute, nil
}

func TestApplicationUsesInjectedRateLimitStore(t *testing.T) {
	users := user.NewMemoryRepository()
	store := &appCounterStore{}
	service := newAuthService(newMemoryIdentityRepository(users), auth.NewMemoryRepository(), testConfig())
	application := NewWithDependencies(slog.Default(), Dependencies{
		UserRepository:       users,
		OrderRepository:      order.NewMemoryRepository(),
		AuthService:          service,
		RateLimits:           security.Limits{LoginPerMinute: 5, RefreshPerMinute: 20, APIPerMinute: 10},
		RateLimitStore:       store,
		RateLimitEnvironment: "test",
	})
	t.Cleanup(func() { _ = application.Close(context.Background()) })

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "203.0.113.10:1234"
		rec := httptest.NewRecorder()
		application.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i+1, rec.Code)
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.counts["user-order-api:test:rate:api:203.0.113.10"] != 2 {
		t.Fatalf("injected store counts = %#v, want two requests", store.counts)
	}
}
