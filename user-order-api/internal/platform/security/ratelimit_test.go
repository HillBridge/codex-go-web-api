package security

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

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
