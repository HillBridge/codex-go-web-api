package main

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestLoadConfigUsesDefaultPort(t *testing.T) {
	config, err := loadConfig(testEnvironment(nil))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.Addr != ":8888" {
		t.Fatalf("Addr = %q, want %q", config.Addr, ":8888")
	}
}

func TestLoadConfigUsesConfiguredPort(t *testing.T) {
	config, err := loadConfig(testEnvironment(map[string]string{"PORT": "9090"}))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.Addr != ":9090" {
		t.Fatalf("Addr = %q, want %q", config.Addr, ":9090")
	}
}

func TestLoadConfigUsesMySQLDSN(t *testing.T) {
	wantDSN := "app:secret@tcp(localhost:3307)/user_order_api?parseTime=true&loc=UTC"
	config, err := loadConfig(func(key string) string {
		if key == "MYSQL_DSN" {
			return wantDSN
		}
		if key == "JWT_SIGNING_KEY" {
			return "test-signing-key-with-at-least-32-bytes"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if config.MySQLDSN != wantDSN {
		t.Fatalf("MySQLDSN = %q, want %q", config.MySQLDSN, wantDSN)
	}
}

func TestLoadConfigRejectsMissingMySQLDSN(t *testing.T) {
	_, err := loadConfig(func(string) string { return "" })
	if err == nil || err.Error() != "MYSQL_DSN is required" {
		t.Fatalf("loadConfig() error = %v, want %q", err, "MYSQL_DSN is required")
	}
}

func TestLoadConfigRejectsMissingJWTSigningKey(t *testing.T) {
	_, err := loadConfig(func(key string) string {
		if key == "MYSQL_DSN" {
			return "app:test@tcp(localhost:3307)/user_order_api?parseTime=true&loc=UTC"
		}
		return ""
	})
	if err == nil || err.Error() != "JWT_SIGNING_KEY is required" {
		t.Fatalf("loadConfig() error = %v, want missing JWT signing key error", err)
	}
}

func TestLoadConfigRejectsShortJWTSigningKey(t *testing.T) {
	_, err := loadConfig(testEnvironment(map[string]string{"JWT_SIGNING_KEY": "too-short"}))
	if err == nil || err.Error() != "JWT_SIGNING_KEY must be at least 32 bytes" {
		t.Fatalf("loadConfig() error = %v, want short JWT signing key error", err)
	}
}

func TestLoadConfigUsesAuthSecurityDefaults(t *testing.T) {
	config, err := loadConfig(testEnvironment(nil))
	if err != nil {
		t.Fatal(err)
	}
	if config.JWTIssuer != "user-order-api" || config.AccessTokenTTL != 15*time.Minute || config.RefreshTokenTTL != 7*24*time.Hour || !config.AuthCookieSecure {
		t.Fatalf("auth defaults = %+v", config)
	}
	if config.LoginRateLimitPerMinute != 5 || config.RefreshRateLimitPerMinute != 20 || config.APIRateLimitPerMinute != 120 {
		t.Fatalf("rate limit defaults = %+v", config)
	}
}

func TestLoadConfigParsesAuthSecuritySettings(t *testing.T) {
	config, err := loadConfig(testEnvironment(map[string]string{
		"JWT_ISSUER":                    "orders.example.com",
		"ACCESS_TOKEN_TTL":              "10m",
		"REFRESH_TOKEN_TTL":             "48h",
		"AUTH_COOKIE_SECURE":            "false",
		"CORS_ALLOWED_ORIGINS":          "https://app.example.com, https://admin.example.com ",
		"RATE_LIMIT_LOGIN_PER_MINUTE":   "7",
		"RATE_LIMIT_REFRESH_PER_MINUTE": "21",
		"RATE_LIMIT_API_PER_MINUTE":     "121",
		"BOOTSTRAP_ADMIN_EMAIL":         "admin@example.com",
		"BOOTSTRAP_ADMIN_PASSWORD":      "correct-password",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.JWTIssuer != "orders.example.com" || config.AccessTokenTTL != 10*time.Minute || config.RefreshTokenTTL != 48*time.Hour || config.AuthCookieSecure {
		t.Fatalf("auth config = %+v", config)
	}
	if len(config.CORSAllowedOrigins) != 2 || config.CORSAllowedOrigins[1] != "https://admin.example.com" {
		t.Fatalf("CORSAllowedOrigins = %#v", config.CORSAllowedOrigins)
	}
	if config.LoginRateLimitPerMinute != 7 || config.RefreshRateLimitPerMinute != 21 || config.APIRateLimitPerMinute != 121 {
		t.Fatalf("rate limits = %+v", config)
	}
	if config.BootstrapAdminEmail != "admin@example.com" || config.BootstrapAdminPassword != "correct-password" {
		t.Fatalf("bootstrap config = %+v", config)
	}
}

func TestLoadConfigRejectsIncompleteBootstrapAdmin(t *testing.T) {
	_, err := loadConfig(testEnvironment(map[string]string{"BOOTSTRAP_ADMIN_EMAIL": "admin@example.com"}))
	if err == nil || err.Error() != "BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD must be set together" {
		t.Fatalf("loadConfig() error = %v", err)
	}
}

func TestLoadConfigRejectsInvalidAllowedOrigin(t *testing.T) {
	_, err := loadConfig(testEnvironment(map[string]string{"CORS_ALLOWED_ORIGINS": "not an origin"}))
	if err == nil || err.Error() != "CORS_ALLOWED_ORIGINS contains an invalid origin" {
		t.Fatalf("loadConfig() error = %v", err)
	}
}

func TestLoadConfigParsesTrustedProxyCIDRs(t *testing.T) {
	config, err := loadConfig(testEnvironment(map[string]string{"TRUSTED_PROXY_CIDRS": "127.0.0.0/8, 10.0.0.0/8"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.TrustedProxyCIDRs) != 2 || config.TrustedProxyCIDRs[0] != netip.MustParsePrefix("127.0.0.0/8") {
		t.Fatalf("TrustedProxyCIDRs = %#v", config.TrustedProxyCIDRs)
	}
}

func TestLoadConfigRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "65536", "not-a-port"} {
		t.Run(port, func(t *testing.T) {
			_, err := loadConfig(func(key string) string {
				if key == "PORT" {
					return port
				}
				return ""
			})
			if err == nil {
				t.Fatal("loadConfig() error = nil, want invalid port error")
			}
		})
	}
}

func TestLoadConfigUsesConfiguredTimeouts(t *testing.T) {
	environment := map[string]string{
		"READ_HEADER_TIMEOUT": "1s",
		"READ_TIMEOUT":        "2s",
		"WRITE_TIMEOUT":       "3s",
		"IDLE_TIMEOUT":        "4s",
		"SHUTDOWN_TIMEOUT":    "5s",
	}

	config, err := loadConfig(testEnvironment(environment))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.ReadHeaderTimeout != time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want %v", config.ReadHeaderTimeout, time.Second)
	}
	if config.ReadTimeout != 2*time.Second {
		t.Errorf("ReadTimeout = %v, want %v", config.ReadTimeout, 2*time.Second)
	}
	if config.WriteTimeout != 3*time.Second {
		t.Errorf("WriteTimeout = %v, want %v", config.WriteTimeout, 3*time.Second)
	}
	if config.IdleTimeout != 4*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", config.IdleTimeout, 4*time.Second)
	}
	if config.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", config.ShutdownTimeout, 5*time.Second)
	}
}

func TestLoadConfigRejectsInvalidTimeout(t *testing.T) {
	_, err := loadConfig(func(key string) string {
		if key == "WRITE_TIMEOUT" {
			return "0s"
		}
		return ""
	})
	if err == nil {
		t.Fatal("loadConfig() error = nil, want invalid timeout error")
	}
}

func TestNewHTTPServerAppliesConfiguredTimeouts(t *testing.T) {
	config := serverConfig{
		Addr:              ":9090",
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	server := newHTTPServer(config, handler)

	if server.Addr != config.Addr {
		t.Errorf("Addr = %q, want %q", server.Addr, config.Addr)
	}
	if server.ReadHeaderTimeout != config.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, config.ReadHeaderTimeout)
	}
	if server.ReadTimeout != config.ReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", server.ReadTimeout, config.ReadTimeout)
	}
	if server.WriteTimeout != config.WriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", server.WriteTimeout, config.WriteTimeout)
	}
	if server.IdleTimeout != config.IdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", server.IdleTimeout, config.IdleTimeout)
	}
}

func TestServeUntilCancelledShutsDownServer(t *testing.T) {
	listener := newBlockingListener()
	defer listener.Close()

	server := newHTTPServer(serverConfig{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveUntilCancelled(ctx, server, listener, time.Second)
	}()

	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not begin accepting connections")
	}

	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serveUntilCancelled() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveUntilCancelled() did not return after context cancellation")
	}
}

type blockingListener struct {
	accepted     chan struct{}
	closed       chan struct{}
	acceptOnce   sync.Once
	shutdownOnce sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{
		accepted: make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() { close(l.accepted) })
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	l.shutdownOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *blockingListener) Addr() net.Addr {
	return testAddr("listener")
}

type testAddr string

func (a testAddr) Network() string { return string(a) }

func (a testAddr) String() string { return string(a) }

func testEnvironment(values map[string]string) func(string) string {
	return func(key string) string {
		if key == "MYSQL_DSN" {
			return "app:test@tcp(localhost:3307)/user_order_api?parseTime=true&loc=UTC"
		}
		if key == "JWT_SIGNING_KEY" && values[key] == "" {
			return "test-signing-key-with-at-least-32-bytes"
		}
		return values[key]
	}
}
