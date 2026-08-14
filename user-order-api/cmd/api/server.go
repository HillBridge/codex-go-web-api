package main

import (
	"log/slog"
	"net/http"
	"os"

	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/user"
)

func newServer() http.Handler {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	auditLogger := audit.NewAsyncLogger(logger)

	userRepo := user.NewMemoryRepository()
	userService := user.NewService(userRepo, auditLogger)
	userHandler := user.NewHandler(userService)

	orderRepo := order.NewMemoryRepository()
	orderService := order.NewService(orderRepo, userService, auditLogger)
	orderHandler := order.NewHandler(orderService)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	userHandler.Register(mux)
	orderHandler.Register(mux)

	return requestLogMiddleware(logger, mux)
}

func requestLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
