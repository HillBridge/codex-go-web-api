package security

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"bridge-go/user-order-api/internal/platform/httpx"
)

type Limits struct {
	LoginPerMinute   int
	RefreshPerMinute int
	APIPerMinute     int
}

type RateLimiter struct {
	limits            Limits
	store             CounterStore
	environment       string
	trustedProxyCIDRs []netip.Prefix
}

func NewRateLimiter(limits Limits, now func() time.Time) *RateLimiter {
	return NewRateLimiterWithStore(limits, now, nil, nil, "local")
}

func NewRateLimiterWithTrustedProxies(limits Limits, now func() time.Time, trustedProxyCIDRs []netip.Prefix) *RateLimiter {
	return NewRateLimiterWithStore(limits, now, trustedProxyCIDRs, nil, "local")
}

func NewRateLimiterWithStore(limits Limits, now func() time.Time, trustedProxyCIDRs []netip.Prefix, store CounterStore, environment string) *RateLimiter {
	if store == nil {
		store = NewMemoryCounterStore(now)
	}
	if strings.TrimSpace(environment) == "" {
		environment = "local"
	}
	return &RateLimiter{limits: limits, store: store, environment: environment, trustedProxyCIDRs: trustedProxyCIDRs}
}

func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter, err := l.allow(r.Context(), l.clientIP(r), routeClass(r.URL.Path))
		if err != nil {
			httpx.WriteError(w, httpx.ServiceUnavailableCode("RATE_LIMIT_BACKEND_UNAVAILABLE", "rate limit backend unavailable"))
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			httpx.WriteError(w, httpx.TooManyRequestsCode("RATE_LIMITED", "too many requests"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *RateLimiter) allow(ctx context.Context, ip, class string) (bool, int, error) {
	limit := l.limits.APIPerMinute
	if class == "login" {
		limit = l.limits.LoginPerMinute
	} else if class == "refresh" {
		limit = l.limits.RefreshPerMinute
	}
	key := "user-order-api:" + l.environment + ":rate:" + class + ":" + ip
	count, ttl, err := l.store.Increment(ctx, key, time.Minute)
	if err != nil {
		return false, 0, err
	}
	if count <= int64(limit) {
		return true, 0, nil
	}
	retryAfter := int((ttl + time.Second - 1) / time.Second)
	if retryAfter < 1 {
		retryAfter = 1
	}
	return false, retryAfter, nil
}

func routeClass(path string) string {
	if strings.HasSuffix(path, "/auth/login") || strings.HasSuffix(path, "/auth/register") {
		return "login"
	}
	if strings.HasSuffix(path, "/auth/refresh") {
		return "refresh"
	}
	return "api"
}

func (l *RateLimiter) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if remote, parseErr := netip.ParseAddr(host); parseErr == nil && l.isTrustedProxy(remote) {
			if forwardedFor := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwardedFor != "" {
				return forwardedFor
			}
		}
		return host
	}
	return r.RemoteAddr
}

func (l *RateLimiter) isTrustedProxy(address netip.Addr) bool {
	for _, prefix := range l.trustedProxyCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
