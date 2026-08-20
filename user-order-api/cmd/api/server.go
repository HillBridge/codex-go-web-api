package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/user"
)

type application struct {
	handler     http.Handler
	auditLogger *audit.AsyncLogger
}

const apiV1Prefix = "/api/v1"

func newServer() *application {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	return newApplication(logger, user.NewMemoryRepository(), order.NewMemoryRepository())
}

func newApplication(logger *slog.Logger, userRepo user.Repository, orderRepo order.Repository) *application {
	auditLogger := audit.NewAsyncLogger(logger)

	userService := user.NewService(userRepo, auditLogger)
	userHandler := user.NewHandler(userService)

	orderService := order.NewService(orderRepo, userRepo, auditLogger)
	orderHandler := order.NewHandler(orderService)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.WriteMethodNotAllowed(w, "GET")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	userHandler.Register(apiMux)
	orderHandler.Register(apiMux)
	apiMux.HandleFunc("/", routeNotFound)

	mux := http.NewServeMux()
	mux.Handle(apiV1Prefix+"/", http.StripPrefix(apiV1Prefix, apiMux))
	mux.HandleFunc("/", routeNotFound)

	return &application{
		handler:     requestIDMiddleware(requestLogMiddleware(logger, recoveryMiddleware(logger, mux))),
		auditLogger: auditLogger,
	}
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
