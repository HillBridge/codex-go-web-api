package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type serverConfig struct {
	MySQLDSN                  string
	OTelServiceName           string
	OTLPGRPCEndpoint          string
	OTLPGRPCInsecure          bool
	JWTSigningKey             string
	JWTIssuer                 string
	AccessTokenTTL            time.Duration
	RefreshTokenTTL           time.Duration
	AuthCookieSecure          bool
	CORSAllowedOrigins        []string
	TrustedProxyCIDRs         []netip.Prefix
	LoginRateLimitPerMinute   int
	RefreshRateLimitPerMinute int
	APIRateLimitPerMinute     int
	RedisAddr                 string
	RedisEnvironment          string
	BootstrapAdminEmail       string
	BootstrapAdminPassword    string
	Addr                      string
	ReadHeaderTimeout         time.Duration
	ReadTimeout               time.Duration
	WriteTimeout              time.Duration
	IdleTimeout               time.Duration
	ShutdownTimeout           time.Duration
	RabbitMQURL               string
	RabbitMQExchange          string
	RabbitMQAuditQueue        string
	OutboxPollInterval        time.Duration
	OutboxBatchSize           int
	OutboxMaxAttempts         int
	ConsumerPrefetch          int
	ConsumerMaxRetries        int
}

func defaultServerConfig() serverConfig {
	return serverConfig{
		Addr:                      ":8888",
		OTelServiceName:           "user-order-api",
		JWTIssuer:                 "user-order-api",
		AccessTokenTTL:            15 * time.Minute,
		RefreshTokenTTL:           7 * 24 * time.Hour,
		AuthCookieSecure:          true,
		LoginRateLimitPerMinute:   5,
		RefreshRateLimitPerMinute: 20,
		APIRateLimitPerMinute:     120,
		RedisEnvironment:          "local",
		ReadHeaderTimeout:         5 * time.Second,
		ReadTimeout:               15 * time.Second,
		WriteTimeout:              15 * time.Second,
		IdleTimeout:               60 * time.Second,
		ShutdownTimeout:           10 * time.Second,
		RabbitMQExchange:          "user-order-api.events",
		RabbitMQAuditQueue:        "user-order-api.audit.v1",
		OutboxPollInterval:        time.Second,
		OutboxBatchSize:           100,
		OutboxMaxAttempts:         10,
		ConsumerPrefetch:          20,
		ConsumerMaxRetries:        5,
	}
}

func loadConfig(getenv func(string) string) (serverConfig, error) {
	config := defaultServerConfig()

	if rawPort := strings.TrimSpace(getenv("PORT")); rawPort != "" {
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return serverConfig{}, fmt.Errorf("PORT must be a number between 1 and 65535")
		}
		config.Addr = ":" + strconv.Itoa(port)
	}

	for _, item := range []struct {
		name   string
		target *time.Duration
	}{
		{name: "READ_HEADER_TIMEOUT", target: &config.ReadHeaderTimeout},
		{name: "READ_TIMEOUT", target: &config.ReadTimeout},
		{name: "WRITE_TIMEOUT", target: &config.WriteTimeout},
		{name: "IDLE_TIMEOUT", target: &config.IdleTimeout},
		{name: "SHUTDOWN_TIMEOUT", target: &config.ShutdownTimeout},
	} {
		raw := strings.TrimSpace(getenv(item.name))
		if raw == "" {
			continue
		}

		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			return serverConfig{}, fmt.Errorf("%s must be a positive Go duration", item.name)
		}
		*item.target = duration
	}

	for _, item := range []struct {
		name   string
		target *time.Duration
	}{
		{name: "ACCESS_TOKEN_TTL", target: &config.AccessTokenTTL},
		{name: "REFRESH_TOKEN_TTL", target: &config.RefreshTokenTTL},
	} {
		raw := strings.TrimSpace(getenv(item.name))
		if raw == "" {
			continue
		}
		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			return serverConfig{}, fmt.Errorf("%s must be a positive Go duration", item.name)
		}
		*item.target = duration
	}

	if raw := strings.TrimSpace(getenv("JWT_ISSUER")); raw != "" {
		config.JWTIssuer = raw
	}
	if raw := strings.TrimSpace(getenv("OTEL_SERVICE_NAME")); raw != "" {
		config.OTelServiceName = raw
	}
	config.OTLPGRPCEndpoint = strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	config.RedisAddr = strings.TrimSpace(getenv("REDIS_ADDR"))
	config.RabbitMQURL = strings.TrimSpace(getenv("RABBITMQ_URL"))
	if raw := strings.TrimSpace(getenv("RABBITMQ_EXCHANGE")); raw != "" {
		config.RabbitMQExchange = raw
	}
	if raw := strings.TrimSpace(getenv("RABBITMQ_AUDIT_QUEUE")); raw != "" {
		config.RabbitMQAuditQueue = raw
	}
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{
		{name: "OUTBOX_POLL_INTERVAL", target: &config.OutboxPollInterval},
	} {
		if raw := strings.TrimSpace(getenv(item.name)); raw != "" {
			duration, err := time.ParseDuration(raw)
			if err != nil || duration <= 0 {
				return serverConfig{}, fmt.Errorf("%s must be a positive Go duration", item.name)
			}
			*item.target = duration
		}
	}
	for _, item := range []struct {
		name   string
		target *int
		max    int
	}{
		{name: "OUTBOX_BATCH_SIZE", target: &config.OutboxBatchSize, max: 1000},
		{name: "OUTBOX_MAX_ATTEMPTS", target: &config.OutboxMaxAttempts, max: 1000},
		{name: "CONSUMER_PREFETCH", target: &config.ConsumerPrefetch, max: 10000},
		{name: "CONSUMER_MAX_RETRIES", target: &config.ConsumerMaxRetries, max: 1000},
	} {
		if raw := strings.TrimSpace(getenv(item.name)); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 || value > item.max {
				return serverConfig{}, fmt.Errorf("%s must be a positive integer up to %d", item.name, item.max)
			}
			*item.target = value
		}
	}
	if raw := strings.TrimSpace(getenv("REDIS_ENVIRONMENT")); raw != "" {
		if strings.IndexAny(raw, " \t\r\n") >= 0 {
			return serverConfig{}, fmt.Errorf("REDIS_ENVIRONMENT must not contain whitespace")
		}
		config.RedisEnvironment = raw
	}
	if raw := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_INSECURE")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return serverConfig{}, fmt.Errorf("OTEL_EXPORTER_OTLP_INSECURE must be true or false")
		}
		config.OTLPGRPCInsecure = value
	}
	if raw := strings.TrimSpace(getenv("AUTH_COOKIE_SECURE")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return serverConfig{}, fmt.Errorf("AUTH_COOKIE_SECURE must be true or false")
		}
		config.AuthCookieSecure = value
	}
	config.CORSAllowedOrigins = splitCSV(getenv("CORS_ALLOWED_ORIGINS"))
	for _, origin := range config.CORSAllowedOrigins {
		if !validOrigin(origin) {
			return serverConfig{}, fmt.Errorf("CORS_ALLOWED_ORIGINS contains an invalid origin")
		}
	}
	for _, raw := range splitCSV(getenv("TRUSTED_PROXY_CIDRS")) {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return serverConfig{}, fmt.Errorf("TRUSTED_PROXY_CIDRS contains an invalid CIDR")
		}
		config.TrustedProxyCIDRs = append(config.TrustedProxyCIDRs, prefix)
	}
	for _, item := range []struct {
		name   string
		target *int
	}{
		{name: "RATE_LIMIT_LOGIN_PER_MINUTE", target: &config.LoginRateLimitPerMinute},
		{name: "RATE_LIMIT_REFRESH_PER_MINUTE", target: &config.RefreshRateLimitPerMinute},
		{name: "RATE_LIMIT_API_PER_MINUTE", target: &config.APIRateLimitPerMinute},
	} {
		raw := strings.TrimSpace(getenv(item.name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return serverConfig{}, fmt.Errorf("%s must be a positive integer", item.name)
		}
		*item.target = value
	}
	config.BootstrapAdminEmail = strings.TrimSpace(getenv("BOOTSTRAP_ADMIN_EMAIL"))
	config.BootstrapAdminPassword = getenv("BOOTSTRAP_ADMIN_PASSWORD")
	if (config.BootstrapAdminEmail == "") != (config.BootstrapAdminPassword == "") {
		return serverConfig{}, fmt.Errorf("BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD must be set together")
	}

	config.MySQLDSN = strings.TrimSpace(getenv("MYSQL_DSN"))
	if config.MySQLDSN == "" {
		return serverConfig{}, fmt.Errorf("MYSQL_DSN is required")
	}
	config.JWTSigningKey = strings.TrimSpace(getenv("JWT_SIGNING_KEY"))
	if config.JWTSigningKey == "" {
		return serverConfig{}, fmt.Errorf("JWT_SIGNING_KEY is required")
	}
	if len(config.JWTSigningKey) < 32 {
		return serverConfig{}, fmt.Errorf("JWT_SIGNING_KEY must be at least 32 bytes")
	}

	return config, nil
}

func splitCSV(raw string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(item); value != "" {
			items = append(items, value)
		}
	}
	return items
}

func validOrigin(raw string) bool {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	return parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func newHTTPServer(config serverConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              config.Addr,
		Handler:           handler,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
	}
}

func serveUntilCancelled(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration) error {
	serveResult := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()

	select {
	case err := <-serveResult:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serveResult
	}
}
