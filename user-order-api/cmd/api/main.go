package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bridge-go/user-order-api/internal/app"
	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/platform/security"
	"bridge-go/user-order-api/internal/platform/telemetry"
)

func main() {
	config, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	telemetryCtx, cancelTelemetry := context.WithTimeout(context.Background(), 10*time.Second)
	runtime, err := telemetry.New(telemetryCtx, telemetry.Config{
		ServiceName:      config.OTelServiceName,
		OTLPGRPCEndpoint: config.OTLPGRPCEndpoint,
		Insecure:         config.OTLPGRPCInsecure,
	})
	cancelTelemetry()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer cancel()
		if err := runtime.Shutdown(shutdownCtx); err != nil {
			log.Printf("telemetry shutdown failed: %v", err)
		}
	}()

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.Open(startupCtx, config.MySQLDSN, runtime.TracerProvider())
	cancelStartup()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("MySQL shutdown failed: %v", err)
		}
	}()

	startupCtx, cancelStartup = context.WithTimeout(context.Background(), 10*time.Second)
	err = database.ApplyMigrations(startupCtx, db)
	cancelStartup()
	if err != nil {
		log.Fatal(err)
	}

	var rateLimitStore security.CounterStore
	var redisStore *security.RedisCounterStore
	if config.RedisAddr != "" {
		startupCtx, cancelStartup = context.WithTimeout(context.Background(), 10*time.Second)
		redisStore, err = security.NewRedisCounterStore(startupCtx, config.RedisAddr, config.RedisEnvironment)
		cancelStartup()
		if err != nil {
			log.Fatal(err)
		}
		rateLimitStore = redisStore
		defer func() {
			if err := redisStore.Close(); err != nil {
				log.Printf("Redis shutdown failed: %v", err)
			}
		}()
	}

	listener, err := net.Listen("tcp", config.Addr)
	if err != nil {
		log.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	startupCtx, cancelStartup = context.WithTimeout(context.Background(), 10*time.Second)
	application, err := app.NewProduction(startupCtx, db, logger, app.Config{
		JWTSigningKey: config.JWTSigningKey, JWTIssuer: config.JWTIssuer,
		AccessTokenTTL: config.AccessTokenTTL, RefreshTokenTTL: config.RefreshTokenTTL,
		AuthCookieSecure: config.AuthCookieSecure, CORSAllowedOrigins: config.CORSAllowedOrigins,
		TrustedProxyCIDRs:       config.TrustedProxyCIDRs,
		LoginRateLimitPerMinute: config.LoginRateLimitPerMinute, RefreshRateLimitPerMinute: config.RefreshRateLimitPerMinute, APIRateLimitPerMinute: config.APIRateLimitPerMinute,
		RateLimitStore: rateLimitStore, RateLimitEnvironment: config.RedisEnvironment,
		BootstrapAdminEmail: config.BootstrapAdminEmail, BootstrapAdminPassword: config.BootstrapAdminPassword,
		RabbitMQURL: config.RabbitMQURL, RabbitMQExchange: config.RabbitMQExchange, RabbitMQAuditQueue: config.RabbitMQAuditQueue,
		OutboxPollInterval: config.OutboxPollInterval, OutboxBatchSize: config.OutboxBatchSize, OutboxMaxAttempts: config.OutboxMaxAttempts,
		ConsumerPrefetch: config.ConsumerPrefetch, ConsumerMaxRetries: config.ConsumerMaxRetries,
	})
	cancelStartup()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer cancel()
		if err := application.Close(shutdownCtx); err != nil {
			log.Printf("audit shutdown failed: %v", err)
		}
	}()

	server := newHTTPServer(config, telemetry.HTTPHandler(application, runtime.TracerProvider()))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("user-order-api listening on http://%s", listener.Addr())
	if err := serveUntilCancelled(ctx, server, listener, config.ShutdownTimeout); err != nil {
		log.Fatal(err)
	}
}
