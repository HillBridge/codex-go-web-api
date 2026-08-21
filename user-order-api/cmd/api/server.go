package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"bridge-go/user-order-api/internal/auth"
	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/platform/security"
	"bridge-go/user-order-api/internal/user"
)

type application struct {
	handler     http.Handler
	auditLogger *audit.AsyncLogger
}

const apiV1Prefix = "/api/v1"

func newServer() *application {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	userRepo := user.NewMemoryRepository()
	return newApplication(logger, userRepo, order.NewMemoryRepository(), newMemoryAuthService(userRepo), false)
}

func newApplication(logger *slog.Logger, userRepo user.Repository, orderRepo order.Repository, authService *auth.Service, cookieSecure bool) *application {
	return newApplicationWithSecurity(logger, userRepo, orderRepo, authService, cookieSecure, nil, nil, security.Limits{LoginPerMinute: 5, RefreshPerMinute: 20, APIPerMinute: 120})
}

func newApplicationWithSecurity(logger *slog.Logger, userRepo user.Repository, orderRepo order.Repository, authService *auth.Service, cookieSecure bool, corsAllowedOrigins []string, trustedProxyCIDRs []netip.Prefix, rateLimits security.Limits) *application {
	auditLogger := audit.NewAsyncLogger(logger)
	authService.SetAuditLogger(auditLogger)

	userService := user.NewService(userRepo, auditLogger)
	userHandler := user.NewHandler(userService)

	orderService := order.NewService(orderRepo, userRepo, auditLogger)
	orderHandler := order.NewHandler(orderService)
	authHandler := auth.NewHandler(authService, cookieSecure)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.WriteMethodNotAllowed(w, "GET")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	authHandler.Register(apiMux)
	protectedMux := http.NewServeMux()
	userHandler.Register(protectedMux)
	orderHandler.Register(protectedMux)
	protected := authService.RequireBearer(protectedMux)
	apiMux.Handle("/users", protected)
	apiMux.Handle("/users/", protected)
	apiMux.Handle("/orders", protected)
	apiMux.Handle("/orders/", protected)
	apiMux.HandleFunc("/", routeNotFound)

	mux := http.NewServeMux()
	mux.Handle(apiV1Prefix+"/", http.StripPrefix(apiV1Prefix, apiMux))
	mux.HandleFunc("/", routeNotFound)
	secured := security.CORSMiddleware(corsAllowedOrigins, security.NewRateLimiterWithTrustedProxies(rateLimits, time.Now, trustedProxyCIDRs).Middleware(mux))

	return &application{
		handler:     requestIDMiddleware(requestLogMiddleware(logger, recoveryMiddleware(logger, secured))),
		auditLogger: auditLogger,
	}
}

func newMemoryAuthService(userRepo user.Repository) *auth.Service {
	now := time.Now
	return auth.NewService(
		newMemoryIdentityRepository(userRepo),
		auth.NewMemoryRepository(),
		auth.NewTokenManager([]byte("test-signing-key-that-is-at-least-32-bytes"), "user-order-api", 15*time.Minute, now),
		7*24*time.Hour,
		now,
	)
}

type memoryIdentityRepository struct {
	users   user.Repository
	mu      sync.RWMutex
	items   map[int64]auth.Identity
	byEmail map[string]int64
}

func newMemoryIdentityRepository(users user.Repository) *memoryIdentityRepository {
	return &memoryIdentityRepository{users: users, items: make(map[int64]auth.Identity), byEmail: make(map[string]int64)}
}

func (r *memoryIdentityRepository) CreateIdentity(ctx context.Context, input auth.NewIdentity) (auth.Identity, error) {
	created, err := r.users.Create(ctx, user.CreateUserRequest{Name: input.Name, Email: input.Email})
	if err != nil {
		if errors.Is(err, user.ErrEmailTaken) {
			return auth.Identity{}, auth.ErrEmailTaken
		}
		return auth.Identity{}, err
	}
	role := input.Role
	if role == "" {
		role = auth.RoleUser
	}
	identity := auth.Identity{ID: created.ID, Name: created.Name, Email: created.Email, PasswordHash: input.PasswordHash, Role: role, AuthVersion: 1, CreatedAt: created.CreatedAt}
	r.mu.Lock()
	r.items[identity.ID] = identity
	r.byEmail[strings.ToLower(identity.Email)] = identity.ID
	r.mu.Unlock()
	return identity, nil
}

func (r *memoryIdentityRepository) FindIdentityByEmail(_ context.Context, email string) (auth.Identity, error) {
	r.mu.RLock()
	id, ok := r.byEmail[strings.ToLower(strings.TrimSpace(email))]
	item := r.items[id]
	r.mu.RUnlock()
	if !ok {
		return auth.Identity{}, auth.ErrIdentityNotFound
	}
	return item, nil
}

func (r *memoryIdentityRepository) FindIdentityByID(_ context.Context, id int64) (auth.Identity, error) {
	r.mu.RLock()
	item, ok := r.items[id]
	r.mu.RUnlock()
	if !ok {
		return auth.Identity{}, auth.ErrIdentityNotFound
	}
	return item, nil
}

func routeNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, httpx.NotFoundCode("ROUTE_NOT_FOUND", "route not found"))
}

func (a *application) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.ServeHTTP(w, r)
}

func (a *application) Close(ctx context.Context) error {
	return a.auditLogger.Close(ctx)
}

func requestLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		logger.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.statusCode(),
			"duration", time.Since(started),
			"request_id", requestIDFromContext(r.Context()),
		)
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newRequestID()
		}

		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func recoveryMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(r.Context(), "panic recovered", "panic", recovered, "request_id", requestIDFromContext(r.Context()))
				httpx.WriteError(w, httpx.Internal("internal server error", fmt.Errorf("panic: %v", recovered)))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(value)
}

func (w *statusRecorder) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

type requestIDContextKey struct{}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

func newRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err == nil {
		return hex.EncodeToString(bytes)
	}

	return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
}
