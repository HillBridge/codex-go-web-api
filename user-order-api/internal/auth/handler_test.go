package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerRegisterSetsRefreshCookieAndReturnsAccessToken(t *testing.T) {
	handler := NewHandler(newTestService(t), false)
	mux := http.NewServeMux()
	handler.Register(mux)
	body := bytes.NewBufferString(`{"name":"Ada","email":"ada@example.com","password":"correct-password"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/register", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != refreshCookieName || !cookie[0].HttpOnly || cookie[0].Secure {
		t.Fatalf("refresh cookie = %+v, want one HttpOnly non-Secure local cookie", cookie)
	}
	var response struct {
		AccessToken string `json:"accessToken"`
		User        struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.AccessToken == "" || response.User.Email != "ada@example.com" {
		t.Fatalf("response = %+v", response)
	}
}

func TestHandlerLoginDoesNotRevealCredentialFailure(t *testing.T) {
	handler := NewHandler(newTestService(t), false)
	mux := http.NewServeMux()
	handler.Register(mux)
	for _, body := range []string{`{"email":"missing@example.com","password":"correct-password"}`, `{"email":"bad","password":"wrong-password"}`} {
		req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "INVALID_CREDENTIALS") {
			t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestHandlerMeRequiresBearerAndReturnsCurrentIdentity(t *testing.T) {
	handler := NewHandler(newTestService(t), false)
	mux := http.NewServeMux()
	handler.Register(mux)
	register := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(`{"name":"Ada","email":"ada@example.com","password":"correct-password"}`))
	register.Header.Set("Content-Type", "application/json")
	registered := httptest.NewRecorder()
	mux.ServeHTTP(registered, register)
	var response struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(registered.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d, want 401", missing.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+response.AccessToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"ada@example.com"`) {
		t.Fatalf("me response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerRefreshRotatesCookieAndLogoutRevokesIt(t *testing.T) {
	handler := NewHandler(newTestService(t), false)
	mux := http.NewServeMux()
	handler.Register(mux)
	register := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(`{"name":"Ada","email":"ada@example.com","password":"correct-password"}`))
	register.Header.Set("Content-Type", "application/json")
	registered := httptest.NewRecorder()
	mux.ServeHTTP(registered, register)
	oldCookie := registered.Result().Cookies()[0]

	refresh := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	refresh.AddCookie(oldCookie)
	refreshed := httptest.NewRecorder()
	mux.ServeHTTP(refreshed, refresh)
	if refreshed.Code != http.StatusOK || len(refreshed.Result().Cookies()) != 1 {
		t.Fatalf("refresh response = %d %s", refreshed.Code, refreshed.Body.String())
	}

	reused := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	reused.AddCookie(oldCookie)
	reusedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(reusedRecorder, reused)
	if reusedRecorder.Code != http.StatusUnauthorized || !strings.Contains(reusedRecorder.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("reused response = %d %s", reusedRecorder.Code, reusedRecorder.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logout.AddCookie(refreshed.Result().Cookies()[0])
	logoutRecorder := httptest.NewRecorder()
	mux.ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", logoutRecorder.Code)
	}
}

func TestBearerMiddlewareAddsPrincipalAndRejectsMissingToken(t *testing.T) {
	service := newTestService(t)
	result, err := service.Register(context.Background(), RegisterRequest{Name: "Ada", Email: "ada@example.com", Password: "correct-password"})
	if err != nil {
		t.Fatal(err)
	}
	handler := service.RequireBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.UserID != result.Identity.ID {
			t.Fatal("missing principal")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing status = %d", missing.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+result.AccessToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("authenticated status = %d", rec.Code)
	}
}

func TestBearerMiddlewareRejectsAccessTokenAfterLogout(t *testing.T) {
	service := newTestService(t)
	result, err := service.Register(context.Background(), RegisterRequest{Name: "Ada", Email: "ada@example.com", Password: "correct-password"})
	if err != nil {
		t.Fatal(err)
	}
	handler := service.RequireBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+result.AccessToken)
	beforeLogout := httptest.NewRecorder()
	handler.ServeHTTP(beforeLogout, request)
	if beforeLogout.Code != http.StatusNoContent {
		t.Fatalf("before logout status = %d, want 204", beforeLogout.Code)
	}

	if err := service.Logout(context.Background(), result.RefreshToken); err != nil {
		t.Fatal(err)
	}
	afterLogout := httptest.NewRecorder()
	handler.ServeHTTP(afterLogout, request)
	if afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("after logout status = %d, want 401", afterLogout.Code)
	}
}
