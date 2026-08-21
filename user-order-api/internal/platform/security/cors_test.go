package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddlewareAllowsOnlyConfiguredOrigin(t *testing.T) {
	handler := CORSMiddleware([]string{"https://app.example.com"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	allowed := httptest.NewRequest(http.MethodGet, "/orders", nil)
	allowed.Header.Set("Origin", "https://app.example.com")
	allowedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" || allowedRecorder.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("allowed CORS headers = %#v", allowedRecorder.Header())
	}

	denied := httptest.NewRequest(http.MethodGet, "/orders", nil)
	denied.Header.Set("Origin", "https://evil.example.com")
	deniedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(deniedRecorder, denied)
	if deniedRecorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("denied CORS origin = %q", deniedRecorder.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddlewareHandlesAllowedPreflight(t *testing.T) {
	handler := CORSMiddleware([]string{"https://app.example.com"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { t.Fatal("preflight reached application") }))
	req := httptest.NewRequest(http.MethodOptions, "/orders", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("preflight = %d %#v", rec.Code, rec.Header())
	}
}
