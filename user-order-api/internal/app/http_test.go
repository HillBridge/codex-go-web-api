package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/platform/testdb"
	"bridge-go/user-order-api/internal/user"
)

func TestUserAndOrderFlow(t *testing.T) {
	server := newTestServer(t)
	accessToken, userID := registerAccessToken(t, server, "ada@example.com")

	orderBody := postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{
		"userId": userID,
		"amount": 2599,
	}, "Authorization", "Bearer "+accessToken, http.StatusCreated)

	var createdOrder struct {
		ID     int64  `json:"id"`
		UserID int64  `json:"userId"`
		Amount int64  `json:"amount"`
		Status string `json:"status"`
	}
	decodeBody(t, orderBody, &createdOrder)
	if createdOrder.ID != 1 || createdOrder.UserID != userID || createdOrder.Status != "pending" {
		t.Fatalf("unexpected order: %+v", createdOrder)
	}

	getWithHeader(t, server, "/api/v1/users/1", "Authorization", "Bearer "+accessToken, http.StatusOK)
	getWithHeader(t, server, "/api/v1/orders/1", "Authorization", "Bearer "+accessToken, http.StatusOK)
}

func TestOrderCreateReplaysIdempotencyKey(t *testing.T) {
	server := newTestServer(t)
	accessToken, userID := registerAccessToken(t, server, "ada@example.com")

	payload := map[string]any{"userId": userID, "amount": 2599}
	first := postJSONWithHeaders(t, server, "/api/v1/orders", payload, map[string]string{"Authorization": "Bearer " + accessToken, "Idempotency-Key": "order-key-1"}, http.StatusCreated)
	second := postJSONWithHeaders(t, server, "/api/v1/orders", payload, map[string]string{"Authorization": "Bearer " + accessToken, "Idempotency-Key": " order-key-1 "}, http.StatusOK)
	var firstOrder, secondOrder struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, first, &firstOrder)
	decodeBody(t, second, &secondOrder)
	if firstOrder.ID != secondOrder.ID {
		t.Fatalf("replayed ID = %d, want %d", secondOrder.ID, firstOrder.ID)
	}
}

func TestOrderCreateRejectsBlankIdempotencyKey(t *testing.T) {
	server := newTestServer(t)
	accessToken, userID := registerAccessToken(t, server, "ada@example.com")

	body := postJSONWithHeaders(t, server, "/api/v1/orders", map[string]any{"userId": userID, "amount": 2599}, map[string]string{"Authorization": "Bearer " + accessToken, "Idempotency-Key": "   "}, http.StatusBadRequest)
	assertErrorCode(t, body, "INVALID_REQUEST")
}

func TestOrderLifecycleHTTPContract(t *testing.T) {
	server := newTestServer(t)
	accessToken, userID := registerAccessToken(t, server, "ada@example.com")
	orderBody := postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"userId": userID, "amount": 2599}, "Authorization", "Bearer "+accessToken, http.StatusCreated)
	var createdOrder struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, orderBody, &createdOrder)

	paid := postJSONWithHeader(t, server, "/api/v1/orders/1/pay", map[string]any{}, "Authorization", "Bearer "+accessToken, http.StatusOK)
	var paidOrder struct {
		Status string `json:"status"`
	}
	decodeBody(t, paid, &paidOrder)
	if paidOrder.Status != "paid" {
		t.Fatalf("paid status = %q, want paid", paidOrder.Status)
	}
	postJSONWithHeader(t, server, "/api/v1/orders/1/pay", map[string]any{}, "Authorization", "Bearer "+accessToken, http.StatusOK)
	body := postJSONWithHeader(t, server, "/api/v1/orders/1/cancel", map[string]any{}, "Authorization", "Bearer "+accessToken, http.StatusConflict)
	assertErrorCode(t, body, "INVALID_ORDER_STATE")
}

func TestDuplicateEmailIsRejected(t *testing.T) {
	server := newTestServer(t)
	registerAccessToken(t, server, "ada@example.com")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"name":"Ada","email":"ada@example.com","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	assertErrorCode(t, body, "EMAIL_ALREADY_EXISTS")
}

func TestUsersListReturnsCursorPagination(t *testing.T) {
	server, adminToken := newTestServerWithBootstrapAdmin(t)
	postJSONWithHeader(t, server, "/api/v1/users", map[string]any{"name": "Ada", "email": "ada@example.com"}, "Authorization", "Bearer "+adminToken, http.StatusCreated)
	postJSONWithHeader(t, server, "/api/v1/users", map[string]any{"name": "Grace", "email": "grace@example.com"}, "Authorization", "Bearer "+adminToken, http.StatusCreated)

	body := getWithHeader(t, server, "/api/v1/users?limit=1", "Authorization", "Bearer "+adminToken, http.StatusOK)
	var response struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	decodeBody(t, body, &response)
	if len(response.Items) != 1 || response.NextCursor != "1" {
		t.Fatalf("list response = %+v, want first user and cursor 1", response)
	}

	body = getWithHeader(t, server, "/api/v1/users?limit=0", "Authorization", "Bearer "+adminToken, http.StatusBadRequest)
	assertErrorCode(t, body, "INVALID_REQUEST")
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
	if got := rec.Body.String(); got != "{\"code\":\"INTERNAL_ERROR\",\"error\":\"internal server error\"}\n" {
		t.Fatalf("body = %q, want internal error JSON", got)
	}
}

func TestUsersMethodNotAllowedReturnsJSONAndAllowHeader(t *testing.T) {
	server := newTestServer(t)
	accessToken, _ := registerAccessToken(t, server, "ada@example.com")
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
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
	if got := rec.Body.String(); got != "{\"code\":\"METHOD_NOT_ALLOWED\",\"error\":\"method not allowed\"}\n" {
		t.Fatalf("body = %q, want method-not-allowed JSON", got)
	}
}

func TestApplicationCloseDrainsAuditEvents(t *testing.T) {
	var output bytes.Buffer
	app := newApplicationForTest(
		slog.New(slog.NewTextHandler(&output, nil)),
		user.NewMemoryRepository(),
		order.NewMemoryRepository(),
		newMemoryAuthService(user.NewMemoryRepository()),
		false,
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

	authService := newMemoryAuthService(userRepo)
	if _, err := authService.BootstrapAdmin(context.Background(), "admin@example.com", "correct-password"); err != nil {
		t.Fatal(err)
	}
	app := newApplicationForTest(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		userRepo,
		order.NewMemoryRepository(),
		authService,
		false,
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := app.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	accessToken := loginAccessToken(t, app, "admin@example.com", "correct-password")
	body := getWithHeader(t, app, "/api/v1/users/1", "Authorization", "Bearer "+accessToken, http.StatusOK)
	var returned struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, body, &returned)
	if returned.ID != created.ID {
		t.Fatalf("returned user ID = %d, want %d", returned.ID, created.ID)
	}
}

func TestLegacyRoutesReturnVersionedRouteNotFoundError(t *testing.T) {
	server := newTestServer(t)
	body := get(t, server, "/users", http.StatusNotFound)
	assertErrorCode(t, body, "ROUTE_NOT_FOUND")
}

func TestProtectedRoutesRejectMissingAccessToken(t *testing.T) {
	server := newTestServer(t)
	body := get(t, server, "/api/v1/users", http.StatusUnauthorized)
	assertErrorCode(t, body, "UNAUTHENTICATED")
}

func TestAuthenticatedCustomerCanCreateOrderForOwnUser(t *testing.T) {
	server := newTestServer(t)
	accessToken, userID := registerAccessToken(t, server, "ada@example.com")

	body := postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"userId": userID, "amount": 2599}, "Authorization", "Bearer "+accessToken, http.StatusCreated)
	var created struct {
		UserID int64 `json:"userId"`
	}
	decodeBody(t, body, &created)
	if created.UserID != userID {
		t.Fatalf("order user ID = %d, want %d", created.UserID, userID)
	}
}

func TestCustomerOrderCreateDefaultsUserIDToCurrentIdentity(t *testing.T) {
	server := newTestServer(t)
	accessToken, userID := registerAccessToken(t, server, "ada@example.com")
	body := postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"amount": 2599}, "Authorization", "Bearer "+accessToken, http.StatusCreated)
	var created struct {
		UserID int64 `json:"userId"`
	}
	decodeBody(t, body, &created)
	if created.UserID != userID {
		t.Fatalf("order user ID = %d, want %d", created.UserID, userID)
	}
}

func TestCustomerCannotReadAnotherCustomersOrder(t *testing.T) {
	server := newTestServer(t)
	adaToken, adaID := registerAccessToken(t, server, "ada@example.com")
	orderBody := postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"userId": adaID, "amount": 2599}, "Authorization", "Bearer "+adaToken, http.StatusCreated)
	var created struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, orderBody, &created)
	graceToken, _ := registerAccessToken(t, server, "grace@example.com")

	body := getWithHeader(t, server, "/api/v1/orders/"+strconv.FormatInt(created.ID, 10), "Authorization", "Bearer "+graceToken, http.StatusForbidden)
	assertErrorCode(t, body, "FORBIDDEN")
}

func TestCustomerCannotCreateOrderForAnotherCustomer(t *testing.T) {
	server := newTestServer(t)
	adaToken, _ := registerAccessToken(t, server, "ada@example.com")
	_, graceID := registerAccessToken(t, server, "grace@example.com")

	body := postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"userId": graceID, "amount": 2599}, "Authorization", "Bearer "+adaToken, http.StatusForbidden)
	assertErrorCode(t, body, "FORBIDDEN")
}

func TestCustomerOrderListOnlyIncludesOwnOrders(t *testing.T) {
	server := newTestServer(t)
	adaToken, adaID := registerAccessToken(t, server, "ada@example.com")
	graceToken, graceID := registerAccessToken(t, server, "grace@example.com")
	postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"userId": adaID, "amount": 100}, "Authorization", "Bearer "+adaToken, http.StatusCreated)
	postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"userId": graceID, "amount": 200}, "Authorization", "Bearer "+graceToken, http.StatusCreated)

	body := getWithHeader(t, server, "/api/v1/orders", "Authorization", "Bearer "+adaToken, http.StatusOK)
	var response struct {
		Items []struct {
			UserID int64 `json:"userId"`
		} `json:"items"`
	}
	decodeBody(t, body, &response)
	if len(response.Items) != 1 || response.Items[0].UserID != adaID {
		t.Fatalf("orders = %+v, want only Ada's order", response.Items)
	}
}

func TestCustomerCannotReadAnotherCustomersProfile(t *testing.T) {
	server := newTestServer(t)
	adaToken, _ := registerAccessToken(t, server, "ada@example.com")
	_, graceID := registerAccessToken(t, server, "grace@example.com")

	body := getWithHeader(t, server, "/api/v1/users/"+strconv.FormatInt(graceID, 10), "Authorization", "Bearer "+adaToken, http.StatusForbidden)
	assertErrorCode(t, body, "FORBIDDEN")
}

func TestCustomerCannotListUsers(t *testing.T) {
	server := newTestServer(t)
	accessToken, _ := registerAccessToken(t, server, "ada@example.com")
	body := getWithHeader(t, server, "/api/v1/users", "Authorization", "Bearer "+accessToken, http.StatusForbidden)
	assertErrorCode(t, body, "FORBIDDEN")
}

func TestCustomerCannotCreateUser(t *testing.T) {
	server := newTestServer(t)
	accessToken, _ := registerAccessToken(t, server, "ada@example.com")
	body := postJSONWithHeader(t, server, "/api/v1/users", map[string]any{"name": "Grace", "email": "grace@example.com"}, "Authorization", "Bearer "+accessToken, http.StatusForbidden)
	assertErrorCode(t, body, "FORBIDDEN")
}

func TestAdminCanListAllOrdersAndCreateForAnotherUser(t *testing.T) {
	server, adminToken := newTestServerWithBootstrapAdmin(t)
	userToken, userID := registerAccessToken(t, server, "ada@example.com")
	postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"amount": 100}, "Authorization", "Bearer "+userToken, http.StatusCreated)
	postJSONWithHeader(t, server, "/api/v1/orders", map[string]any{"userId": userID, "amount": 200}, "Authorization", "Bearer "+adminToken, http.StatusCreated)

	body := getWithHeader(t, server, "/api/v1/orders", "Authorization", "Bearer "+adminToken, http.StatusOK)
	var response struct {
		Items []struct {
			UserID int64 `json:"userId"`
		} `json:"items"`
	}
	decodeBody(t, body, &response)
	if len(response.Items) != 2 || response.Items[0].UserID != userID || response.Items[1].UserID != userID {
		t.Fatalf("admin orders = %+v", response.Items)
	}
}

func TestMySQLAuthenticationAndOrderHTTPFlow(t *testing.T) {
	dsn := testdb.RequireDSN(t, os.Getenv("MYSQL_TEST_DSN"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ApplyMigrations(ctx, db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	app, err := NewProduction(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = app.Close(shutdownCtx)
	})

	email := "http-flow-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.com"
	accessToken, userID := registerAccessToken(t, app, email)
	orderBody := postJSONWithHeader(t, app, "/api/v1/orders", map[string]any{"amount": 2599}, "Authorization", "Bearer "+accessToken, http.StatusCreated)
	var createdOrder struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, orderBody, &createdOrder)

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"`+email+`","password":"correct-password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	app.ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login = %d %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	oldCookie := loginRecorder.Result().Cookies()[0]
	refresh := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	refresh.AddCookie(oldCookie)
	refreshRecorder := httptest.NewRecorder()
	app.ServeHTTP(refreshRecorder, refresh)
	if refreshRecorder.Code != http.StatusOK {
		t.Fatalf("refresh = %d %s", refreshRecorder.Code, refreshRecorder.Body.String())
	}
	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.AddCookie(refreshRecorder.Result().Cookies()[0])
	logoutRecorder := httptest.NewRecorder()
	app.ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}

	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
		_, _ = db.Exec("DELETE FROM orders WHERE id = ?", createdOrder.ID)
		_, _ = db.Exec("DELETE FROM users WHERE id = ?", userID)
	})
}

func TestMySQLSessionRevocationAuthorizationAndAdminHTTPFlow(t *testing.T) {
	dsn := testdb.RequireDSN(t, os.Getenv("MYSQL_TEST_DSN"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ApplyMigrations(ctx, db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	config := testConfig()
	config.BootstrapAdminEmail = "admin-" + suffix + "@example.com"
	config.BootstrapAdminPassword = "correct-password"
	app, err := NewProduction(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = app.Close(shutdownCtx)
	})

	adaAccessToken, adaID, adaRefreshCookie := registerAccessTokenAndCookie(t, app, "ada-"+suffix+"@example.com")
	graceAccessToken, graceID := registerAccessToken(t, app, "grace-"+suffix+"@example.com")
	orderBody := postJSONWithHeader(t, app, "/api/v1/orders", map[string]any{"amount": 2599}, "Authorization", "Bearer "+adaAccessToken, http.StatusCreated)
	var adaOrder struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, orderBody, &adaOrder)
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM sessions WHERE user_id IN (?, ?, (SELECT id FROM users WHERE email = ?))", adaID, graceID, config.BootstrapAdminEmail)
		_, _ = db.Exec("DELETE FROM orders WHERE user_id IN (?, ?)", adaID, graceID)
		_, _ = db.Exec("DELETE FROM users WHERE id IN (?, ?) OR email = ?", adaID, graceID, config.BootstrapAdminEmail)
	})

	forbidden := getWithHeader(t, app, "/api/v1/orders/"+strconv.FormatInt(adaOrder.ID, 10), "Authorization", "Bearer "+graceAccessToken, http.StatusForbidden)
	assertErrorCode(t, forbidden, "FORBIDDEN")

	refresh := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	refresh.AddCookie(adaRefreshCookie)
	refreshRecorder := httptest.NewRecorder()
	app.ServeHTTP(refreshRecorder, refresh)
	if refreshRecorder.Code != http.StatusOK {
		t.Fatalf("refresh = %d %s", refreshRecorder.Code, refreshRecorder.Body.String())
	}
	var refreshed struct {
		AccessToken string `json:"accessToken"`
	}
	decodeBody(t, refreshRecorder.Body.Bytes(), &refreshed)
	oldAccess := getWithHeader(t, app, "/api/v1/auth/me", "Authorization", "Bearer "+adaAccessToken, http.StatusUnauthorized)
	assertErrorCode(t, oldAccess, "UNAUTHENTICATED")

	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.AddCookie(refreshRecorder.Result().Cookies()[0])
	logoutRecorder := httptest.NewRecorder()
	app.ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}
	revokedAccess := getWithHeader(t, app, "/api/v1/auth/me", "Authorization", "Bearer "+refreshed.AccessToken, http.StatusUnauthorized)
	assertErrorCode(t, revokedAccess, "UNAUTHENTICATED")

	adminAccessToken := loginAccessToken(t, app, config.BootstrapAdminEmail, config.BootstrapAdminPassword)
	postJSONWithHeader(t, app, "/api/v1/orders", map[string]any{"userId": graceID, "amount": 3000}, "Authorization", "Bearer "+adminAccessToken, http.StatusCreated)
	adminOrders := getWithHeader(t, app, "/api/v1/orders", "Authorization", "Bearer "+adminAccessToken, http.StatusOK)
	var listed struct {
		Items []struct {
			UserID int64 `json:"userId"`
		} `json:"items"`
	}
	decodeBody(t, adminOrders, &listed)
	var hasAdaOrder, hasGraceOrder bool
	for _, item := range listed.Items {
		hasAdaOrder = hasAdaOrder || item.UserID == adaID
		hasGraceOrder = hasGraceOrder || item.UserID == graceID
	}
	if !hasAdaOrder || !hasGraceOrder {
		t.Fatalf("admin orders = %+v, want Ada and Grace orders", listed.Items)
	}
}

func newTestServer(t *testing.T) *Application {
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

func newTestServerWithBootstrapAdmin(t *testing.T) (*Application, string) {
	t.Helper()
	userRepo := user.NewMemoryRepository()
	authService := newMemoryAuthService(userRepo)
	if _, err := authService.BootstrapAdmin(context.Background(), "admin@example.com", "correct-password"); err != nil {
		t.Fatal(err)
	}
	server := newApplicationForTest(slog.New(slog.NewTextHandler(io.Discard, nil)), userRepo, order.NewMemoryRepository(), authService, false)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return server, loginAccessToken(t, server, "admin@example.com", "correct-password")
}

func registerAccessToken(t *testing.T, handler http.Handler, email string) (string, int64) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"name":"Ada","email":"`+email+`","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		AccessToken string `json:"accessToken"`
		User        struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	decodeBody(t, rec.Body.Bytes(), &response)
	return response.AccessToken, response.User.ID
}

func registerAccessTokenAndCookie(t *testing.T, handler http.Handler, email string) (string, int64, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"name":"Ada","email":"`+email+`","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("register cookies = %+v, want one refresh cookie", cookies)
	}
	var response struct {
		AccessToken string `json:"accessToken"`
		User        struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	decodeBody(t, rec.Body.Bytes(), &response)
	return response.AccessToken, response.User.ID, cookies[0]
}

func loginAccessToken(t *testing.T, handler http.Handler, email, password string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
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

func postJSON(t *testing.T, handler http.Handler, path string, payload map[string]any, wantStatus int) []byte {
	return postJSONWithHeader(t, handler, path, payload, "", "", wantStatus)
}

func postJSONWithHeader(t *testing.T, handler http.Handler, path string, payload map[string]any, header, value string, wantStatus int) []byte {
	return postJSONWithHeaders(t, handler, path, payload, map[string]string{header: value}, wantStatus)
}

func postJSONWithHeaders(t *testing.T, handler http.Handler, path string, payload map[string]any, headers map[string]string, wantStatus int) []byte {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for header, value := range headers {
		if header != "" {
			req.Header.Set(header, value)
		}
	}
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

func assertErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var response struct {
		Code string `json:"code"`
	}
	decodeBody(t, body, &response)
	if response.Code != want {
		t.Fatalf("error code = %q, want %q; body = %s", response.Code, want, body)
	}
}
