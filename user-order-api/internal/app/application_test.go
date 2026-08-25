package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/auth"
	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/security"
	"bridge-go/user-order-api/internal/user"
)

func TestApplicationUsesProvidedRepositories(t *testing.T) {
	userRepo := user.NewMemoryRepository()
	created, err := userRepo.Create(context.Background(), user.CreateUserRequest{Name: "Ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	authService := auth.NewService(
		auth.NewMemoryIdentityRepository(),
		auth.NewMemoryRepository(),
		auth.NewTokenManager([]byte("test-signing-key-that-is-at-least-32-bytes"), "user-order-api", 15*time.Minute, time.Now),
		time.Hour,
		time.Now,
	)
	if _, err := authService.BootstrapAdmin(context.Background(), "admin@example.com", "correct-password"); err != nil {
		t.Fatal(err)
	}

	application := NewWithDependencies(slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{
		UserRepository:  userRepo,
		OrderRepository: order.NewMemoryRepository(),
		AuthService:     authService,
		CookieSecure:    false,
		RateLimits:      security.Limits{LoginPerMinute: 5, RefreshPerMinute: 20, APIPerMinute: 120},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := application.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	accessToken := loginAccessToken(t, application, "admin@example.com", "correct-password")
	body := getWithHeader(t, application, "/api/v1/users/1", "Authorization", "Bearer "+accessToken, http.StatusOK)
	var returned struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, body, &returned)
	if returned.ID != created.ID {
		t.Fatalf("returned user ID = %d, want %d", returned.ID, created.ID)
	}
}

func loginAccessToken(t *testing.T, handler http.Handler, email, password string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"email":"`+email+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		AccessToken string `json:"accessToken"`
	}
	decodeBody(t, rec.Body.Bytes(), &response)
	return response.AccessToken
}

func getWithHeader(t *testing.T, handler http.Handler, path, header, value string, wantStatus int) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(header, value)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("GET %s status = %d, want %d, body = %s", path, rec.Code, wantStatus, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func decodeBody(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, string(body))
	}
}
