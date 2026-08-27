package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"bridge-go/user-order-api/internal/auth"
	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/platform/observability"
	"bridge-go/user-order-api/internal/platform/security"
	"bridge-go/user-order-api/internal/user"
	"go.opentelemetry.io/otel/trace"
)

const apiV1Prefix = "/api/v1"

type Dependencies struct {
	UserRepository       user.Repository
	OrderRepository      order.Repository
	AuthService          *auth.Service
	Readiness            readinessChecker
	DatabaseStats        observability.DatabaseStats
	CookieSecure         bool
	CORSOrigins          []string
	TrustedProxies       []netip.Prefix
	RateLimits           security.Limits
	RateLimitStore       security.CounterStore
	RateLimitEnvironment string
}

type Application struct {
	handler     http.Handler
	auditLogger *audit.AsyncLogger
	readiness   readinessChecker
	metrics     *observability.Metrics
}

func NewWithDependencies(logger *slog.Logger, deps Dependencies) *Application {
	auditLogger := audit.NewAsyncLogger(logger)
	deps.AuthService.SetAuditLogger(auditLogger)
	metrics, _ := observability.New(deps.DatabaseStats, auditLogger)
	application := &Application{auditLogger: auditLogger, readiness: deps.Readiness, metrics: metrics}

	userHandler := user.NewHandler(user.NewService(deps.UserRepository, auditLogger))
	orderHandler := order.NewHandler(order.NewService(deps.OrderRepository, deps.UserRepository, auditLogger))
	authHandler := auth.NewHandler(deps.AuthService, deps.CookieSecure)

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
	protected := deps.AuthService.RequireBearer(protectedMux)
	apiMux.Handle("/users", protected)
	apiMux.Handle("/users/", protected)
	apiMux.Handle("/orders", protected)
	apiMux.Handle("/orders/", protected)
	apiMux.HandleFunc("/", routeNotFound)

	mux := http.NewServeMux()
	mux.Handle(apiV1Prefix+"/", http.StripPrefix(apiV1Prefix, apiMux))
	mux.HandleFunc("/healthz", application.healthz)
	mux.HandleFunc("/readyz", application.readyz)
	mux.Handle("/metrics", application.metrics.Handler())
	mux.HandleFunc("/", routeNotFound)
	secured := security.CORSMiddleware(
		deps.CORSOrigins,
		security.NewRateLimiterWithStore(deps.RateLimits, time.Now, deps.TrustedProxies, deps.RateLimitStore, deps.RateLimitEnvironment).Middleware(mux),
	)

	application.handler = metrics.Middleware(requestIDMiddleware(requestLogMiddleware(logger, recoveryMiddleware(logger, secured))))
	return application
}

func (a *Application) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.ServeHTTP(w, r)
}

func (a *Application) Close(ctx context.Context) error {
	return a.auditLogger.Close(ctx)
}

func (a *Application) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(w, "GET")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *Application) readyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(w, "GET")
		return
	}
	if a.readiness != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.readiness.PingContext(ctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func routeNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, httpx.NotFoundCode("ROUTE_NOT_FOUND", "route not found"))
}

func requestLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		fields := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.statusCode(),
			"duration", time.Since(started),
			"request_id", requestIDFromContext(r.Context()),
		}
		spanContext := trace.SpanContextFromContext(r.Context())
		if spanContext.IsValid() {
			fields = append(fields, "trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String())
		}
		logger.InfoContext(r.Context(), "request", fields...)
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

type readinessChecker interface {
	PingContext(context.Context) error
}

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
