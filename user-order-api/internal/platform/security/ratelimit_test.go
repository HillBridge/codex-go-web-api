package security

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCounterStore struct {
	mu     sync.Mutex
	counts map[string]int64
	keys   []string
	err    error
}

func (s *fakeCounterStore) Increment(_ context.Context, key string, _ time.Duration) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, 0, s.err
	}
	if s.counts == nil {
		s.counts = make(map[string]int64)
	}
	s.counts[key]++
	s.keys = append(s.keys, key)
	return s.counts[key], time.Minute, nil
}

func TestRateLimiterRejectsSixthLoginFromSameIP(t *testing.T) {
	limiter := NewRateLimiter(Limits{LoginPerMinute: 5, RefreshPerMinute: 20, APIPerMinute: 120}, time.Now)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for attempt := 1; attempt <= 6; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		req.RemoteAddr = "203.0.113.10:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if attempt <= 5 && rec.Code != http.StatusNoContent {
			t.Fatalf("attempt %d status = %d, want 204", attempt, rec.Code)
		}
		if attempt == 6 && (rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "") {
			t.Fatalf("attempt 6 = %d, Retry-After=%q; want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
		}
	}
}

func TestRateLimiterTrustsForwardedForOnlyFromConfiguredProxy(t *testing.T) {
	limiter := NewRateLimiterWithTrustedProxies(Limits{LoginPerMinute: 1, RefreshPerMinute: 20, APIPerMinute: 120}, time.Now, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, forwardedFor := range []string{"203.0.113.10", "203.0.113.11"} {
		req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", forwardedFor)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("trusted proxy request for %s = %d, want 204", forwardedFor, rec.Code)
		}
	}
}

func TestRateLimiterSharesCounterStore(t *testing.T) {
	store := &fakeCounterStore{}
	limits := Limits{LoginPerMinute: 5, RefreshPerMinute: 20, APIPerMinute: 1}
	first := NewRateLimiterWithStore(limits, time.Now, nil, store, "test")
	second := NewRateLimiterWithStore(limits, time.Now, nil, store, "test")
	makeHandler := func(limiter *RateLimiter) http.Handler {
		return limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	}

	for _, handler := range []http.Handler{makeHandler(first), makeHandler(second)} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = "203.0.113.10:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("unexpected status = %d", rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec := httptest.NewRecorder()
	makeHandler(second).ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request status = %d, want 429", rec.Code)
	}
}

func TestRateLimiterReturnsBackendUnavailable(t *testing.T) {
	store := &fakeCounterStore{err: errors.New("redis password=secret address=private")}
	limiter := NewRateLimiterWithStore(Limits{APIPerMinute: 1}, time.Now, nil, store, "test")
	called := false
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "RATE_LIMIT_BACKEND_UNAVAILABLE") {
		t.Fatalf("body = %q, want backend error code", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private") || called {
		t.Fatalf("body leaked backend details or downstream was called: body=%q called=%v", rec.Body.String(), called)
	}
}

func TestRateLimiterKeyContainsOnlyRoutingIdentity(t *testing.T) {
	store := &fakeCounterStore{}
	limiter := NewRateLimiterWithStore(Limits{APIPerMinute: 10}, time.Now, nil, store, "test")
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Authorization", "Bearer sensitive-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.keys) != 1 || store.keys[0] != "user-order-api:test:rate:api:203.0.113.10" {
		t.Fatalf("keys = %#v, want exact routing key", store.keys)
	}
}
