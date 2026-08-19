package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/user"
)

func TestUserAndOrderFlow(t *testing.T) {
	server := newTestServer(t)

	userBody := postJSON(t, server, "/users", map[string]any{
		"name":  "Ada",
		"email": "ada@example.com",
	}, http.StatusCreated)

	var createdUser struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	decodeBody(t, userBody, &createdUser)
	if createdUser.ID != 1 || createdUser.Email != "ada@example.com" {
		t.Fatalf("unexpected user: %+v", createdUser)
	}

	orderBody := postJSON(t, server, "/orders", map[string]any{
		"userId": createdUser.ID,
		"amount": 2599,
	}, http.StatusCreated)

	var createdOrder struct {
		ID     int64  `json:"id"`
		UserID int64  `json:"userId"`
		Amount int64  `json:"amount"`
		Status string `json:"status"`
	}
	decodeBody(t, orderBody, &createdOrder)
	if createdOrder.ID != 1 || createdOrder.UserID != createdUser.ID || createdOrder.Status != "pending" {
		t.Fatalf("unexpected order: %+v", createdOrder)
	}

	get(t, server, "/users/1", http.StatusOK)
	get(t, server, "/orders/1", http.StatusOK)
}

func TestRejectsOrderForMissingUser(t *testing.T) {
	server := newTestServer(t)

	postJSON(t, server, "/orders", map[string]any{
		"userId": 99,
		"amount": 100,
	}, http.StatusBadRequest)
}

func TestDuplicateEmailIsRejected(t *testing.T) {
	server := newTestServer(t)

	payload := map[string]any{
		"name":  "Ada",
		"email": "ada@example.com",
	}
	postJSON(t, server, "/users", payload, http.StatusCreated)
	postJSON(t, server, "/users", payload, http.StatusBadRequest)
}

func TestUsersListReturnsCursorPagination(t *testing.T) {
	server := newTestServer(t)
	postJSON(t, server, "/users", map[string]any{"name": "Ada", "email": "ada@example.com"}, http.StatusCreated)
	postJSON(t, server, "/users", map[string]any{"name": "Grace", "email": "grace@example.com"}, http.StatusCreated)

	body := get(t, server, "/users?limit=1", http.StatusOK)
	var response struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	decodeBody(t, body, &response)
	if len(response.Items) != 1 || response.Items[0].ID != 1 || response.NextCursor != "1" {
		t.Fatalf("list response = %+v, want first user and cursor 1", response)
	}

	get(t, server, "/users?limit=0", http.StatusBadRequest)
}

func TestRequestIDMiddlewareAddsResponseRequestID(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("X-Request-ID response header is empty")
	}
}

func TestRequestIDMiddlewarePreservesCallerRequestID(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", "client-request-123")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "client-request-123" {
		t.Fatalf("X-Request-ID = %q, want %q", got, "client-request-123")
	}
}

func TestRecoveryMiddlewareReturnsJSONInternalServerError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := recoveryMiddleware(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("unexpected failure")
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := rec.Body.String(); got != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("body = %q, want internal error JSON", got)
	}
}

func TestUsersMethodNotAllowedReturnsJSONAndAllowHeader(t *testing.T) {
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/users", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("Allow = %q, want %q", got, "GET, POST")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := rec.Body.String(); got != "{\"error\":\"method not allowed\"}\n" {
		t.Fatalf("body = %q, want method-not-allowed JSON", got)
	}
}

func TestApplicationCloseDrainsAuditEvents(t *testing.T) {
	var output bytes.Buffer
	app := newApplication(
		slog.New(slog.NewTextHandler(&output, nil)),
		user.NewMemoryRepository(),
		order.NewMemoryRepository(),
	)
	app.auditLogger.Record(context.Background(), "order.created", map[string]any{"orderID": int64(1)})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if got := output.String(); !strings.Contains(got, "action=order.created") {
		t.Fatalf("audit output = %q, want drained order.created event", got)
	}
}

func TestApplicationUsesProvidedRepositories(t *testing.T) {
	userRepo := user.NewMemoryRepository()
	created, err := userRepo.Create(context.Background(), user.CreateUserRequest{Name: "Ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	app := newApplication(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		userRepo,
		order.NewMemoryRepository(),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := app.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	body := get(t, app, "/users/1", http.StatusOK)
	var returned struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, body, &returned)
	if returned.ID != created.ID {
		t.Fatalf("returned user ID = %d, want %d", returned.ID, created.ID)
	}
}

func newTestServer(t *testing.T) *application {
	t.Helper()
	server := newServer()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return server
}

func postJSON(t *testing.T, handler http.Handler, path string, payload map[string]any, wantStatus int) []byte {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != wantStatus {
		t.Fatalf("POST %s status = %d, want %d, body = %s", path, rec.Code, wantStatus, rec.Body.String())
	}

	return rec.Body.Bytes()
}

func get(t *testing.T, handler http.Handler, path string, wantStatus int) []byte {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
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
