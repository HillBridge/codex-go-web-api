package security

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
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
	now               func() time.Time
	mu                sync.Mutex
	items             map[string]bucket
	trustedProxyCIDRs []netip.Prefix
}

type bucket struct {
	started time.Time
	used    int
}

func NewRateLimiter(limits Limits, now func() time.Time) *RateLimiter {
	return NewRateLimiterWithTrustedProxies(limits, now, nil)
}

func NewRateLimiterWithTrustedProxies(limits Limits, now func() time.Time, trustedProxyCIDRs []netip.Prefix) *RateLimiter {
	return &RateLimiter{limits: limits, now: now, items: make(map[string]bucket), trustedProxyCIDRs: trustedProxyCIDRs}
}

func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := l.allow(l.clientIP(r), routeClass(r.URL.Path))
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			httpx.WriteError(w, httpx.TooManyRequestsCode("RATE_LIMITED", "too many requests"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *RateLimiter) allow(ip, class string) (bool, int) {
	limit := l.limits.APIPerMinute
	if class == "login" {
		limit = l.limits.LoginPerMinute
	} else if class == "refresh" {
		limit = l.limits.RefreshPerMinute
	}
	now := l.now().UTC()
	key := ip + "|" + class
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.items[key]
	if item.started.IsZero() || now.Sub(item.started) >= time.Minute {
		l.items[key] = bucket{started: now, used: 1}
		return true, 0
	}
	if item.used < limit {
		item.used++
		l.items[key] = item
		return true, 0
	}
	remaining := time.Minute - now.Sub(item.started)
	retryAfter := int(remaining.Round(time.Second).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	return false, retryAfter
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
