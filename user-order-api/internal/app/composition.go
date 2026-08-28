package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"bridge-go/user-order-api/internal/auth"
	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/messaging"
	"bridge-go/user-order-api/internal/platform/observability"
	"bridge-go/user-order-api/internal/platform/outbox"
	"bridge-go/user-order-api/internal/platform/security"
	"bridge-go/user-order-api/internal/user"
)

type Config struct {
	JWTSigningKey, JWTIssuer                                                  string
	AccessTokenTTL, RefreshTokenTTL                                           time.Duration
	AuthCookieSecure                                                          bool
	CORSAllowedOrigins                                                        []string
	TrustedProxyCIDRs                                                         []netip.Prefix
	LoginRateLimitPerMinute, RefreshRateLimitPerMinute, APIRateLimitPerMinute int
	RateLimitStore                                                            security.CounterStore
	RateLimitEnvironment                                                      string
	BootstrapAdminEmail, BootstrapAdminPassword                               string
	RabbitMQURL, RabbitMQExchange, RabbitMQAuditQueue                         string
	OutboxPollInterval                                                        time.Duration
	OutboxBatchSize, OutboxMaxAttempts, ConsumerPrefetch, ConsumerMaxRetries  int
}

func NewMemory(ctx context.Context, logger *slog.Logger, config Config) (*Application, error) {
	users := user.NewMemoryRepository()
	service := newAuthService(newMemoryIdentityRepository(users), auth.NewMemoryRepository(), config)
	if err := bootstrapAdmin(ctx, logger, service, config); err != nil {
		return nil, err
	}
	return buildApplication(logger, users, order.NewMemoryRepository(), service, config, nil, nil, nil, nil, nil), nil
}

func NewProduction(ctx context.Context, db *sql.DB, logger *slog.Logger, config Config) (*Application, error) {
	eventRepo := outbox.NewMySQLRepository(db)
	authRepo := auth.NewMySQLRepository(db, eventRepo)
	service := newAuthService(authRepo, authRepo, config)
	if err := bootstrapAdmin(ctx, logger, service, config); err != nil {
		return nil, err
	}
	users := user.NewMySQLRepository(db, eventRepo)
	orders := order.NewMySQLRepository(db, eventRepo)
	var workers []Worker
	var broker messaging.Broker
	if config.RabbitMQURL != "" {
		brokerInstance, err := messaging.NewBroker(ctx, config.RabbitMQURL, config.RabbitMQExchange, config.RabbitMQAuditQueue, config.ConsumerPrefetch)
		if err != nil {
			return nil, err
		}
		broker = brokerInstance
		workers = append(workers,
			outbox.NewPublisher(eventRepo, brokerInstance, logger, "api-publisher", config.OutboxPollInterval, config.OutboxBatchSize, config.OutboxMaxAttempts),
			audit.NewConsumer(eventRepo, brokerInstance, "audit-v1", config.ConsumerMaxRetries, logger, audit.LogEvent),
		)
	}
	var brokerReady func() bool
	if brokerInstance, ok := broker.(*messaging.RabbitBroker); ok {
		brokerReady = brokerInstance.Ready
	}
	return buildApplication(logger, users, orders, service, config, db, db, workers, broker, brokerReady), nil
}

func newAuthService(identities auth.IdentityRepository, sessions auth.Repository, config Config) *auth.Service {
	return auth.NewService(identities, sessions, auth.NewTokenManager([]byte(config.JWTSigningKey), config.JWTIssuer, config.AccessTokenTTL, time.Now), config.RefreshTokenTTL, time.Now)
}

func buildApplication(logger *slog.Logger, users user.Repository, orders order.Repository, service *auth.Service, config Config, readiness readinessChecker, databaseStats observability.DatabaseStats, workers []Worker, broker messaging.Broker, brokerReady func() bool) *Application {
	return NewWithDependencies(logger, Dependencies{UserRepository: users, OrderRepository: orders, AuthService: service, Readiness: readiness, DatabaseStats: databaseStats, CookieSecure: config.AuthCookieSecure, CORSOrigins: config.CORSAllowedOrigins, TrustedProxies: config.TrustedProxyCIDRs, RateLimits: security.Limits{LoginPerMinute: config.LoginRateLimitPerMinute, RefreshPerMinute: config.RefreshRateLimitPerMinute, APIPerMinute: config.APIRateLimitPerMinute}, RateLimitStore: config.RateLimitStore, RateLimitEnvironment: config.RateLimitEnvironment, Workers: workers, Broker: broker, BrokerReady: brokerReady})
}

func bootstrapAdmin(ctx context.Context, logger *slog.Logger, service *auth.Service, config Config) error {
	if config.BootstrapAdminEmail == "" {
		return nil
	}
	created, err := service.BootstrapAdmin(ctx, config.BootstrapAdminEmail, config.BootstrapAdminPassword)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		logger.Info("bootstrap admin account created")
	}
	return nil
}
