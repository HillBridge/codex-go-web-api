package app

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/auth"
	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/security"
	"bridge-go/user-order-api/internal/user"
)

func newServer() *Application {
	application, err := NewMemory(context.Background(), slog.Default(), testConfig())
	if err != nil {
		panic(err)
	}
	return application
}

func newApplication(logger *slog.Logger, users user.Repository, orders order.Repository, service *auth.Service, cookieSecure bool) *Application {
	return NewWithDependencies(logger, Dependencies{UserRepository: users, OrderRepository: orders, AuthService: service, CookieSecure: cookieSecure, RateLimits: security.Limits{LoginPerMinute: 5, RefreshPerMinute: 20, APIPerMinute: 120}})
}

func newMemoryAuthService(users user.Repository) *auth.Service {
	config := testConfig()
	return newAuthService(newMemoryIdentityRepository(users), auth.NewMemoryRepository(), config)
}

func testConfig() Config {
	return Config{JWTSigningKey: "test-signing-key-that-is-at-least-32-bytes", JWTIssuer: "user-order-api", AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour, LoginRateLimitPerMinute: 5, RefreshRateLimitPerMinute: 20, APIRateLimitPerMinute: 120}
}

var _ http.Handler = (*Application)(nil)
var _ = testing.T{}
