package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bridge-go/user-order-api/internal/platform/page"
)

func TestDecodeJSONAcceptsSingleJSONValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Ada"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	var target struct {
		Name string `json:"name"`
	}

	if err := DecodeJSON(rec, req, &target); err != nil {
		t.Fatalf("DecodeJSON() error = %v", err)
	}
	if target.Name != "Ada" {
		t.Fatalf("Name = %q, want %q", target.Name, "Ada")
	}
}

func TestDecodeJSONRejectsNonJSONContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Ada"}`))
	req.Header.Set("Content-Type", "text/plain")

	err := DecodeJSON(httptest.NewRecorder(), req, &struct{}{})
	assertAppError(t, err, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
}

func TestDecodeJSONRejectsTrailingJSONValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Ada"}{}`))
	req.Header.Set("Content-Type", "application/json")
	var target struct {
		Name string `json:"name"`
	}

	err := DecodeJSON(httptest.NewRecorder(), req, &target)
	assertAppError(t, err, http.StatusBadRequest, "request body must contain a single JSON object")
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	body := `{"name":"` + strings.Repeat("a", int(MaxJSONBodyBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	err := DecodeJSON(httptest.NewRecorder(), req, &struct{}{})
	assertAppError(t, err, http.StatusRequestEntityTooLarge, "request body too large")
}

func TestParsePageRequest(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    page.Request
		message string
	}{
		{name: "defaults", want: page.Request{Limit: 20}},
		{name: "explicit cursor", query: "limit=3&afterId=12", want: page.Request{Limit: 3, AfterID: 12}},
		{name: "zero limit", query: "limit=0", message: "limit must be between 1 and 100"},
		{name: "too large limit", query: "limit=101", message: "limit must be between 1 and 100"},
		{name: "zero cursor", query: "afterId=0", message: "afterId must be a positive integer"},
		{name: "invalid cursor", query: "afterId=bad", message: "afterId must be a positive integer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/users?"+test.query, nil)
			got, err := ParsePageRequest(req)
			if test.message != "" {
				assertAppError(t, err, http.StatusBadRequest, test.message)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("page request = %+v, want %+v", got, test.want)
			}
		})
	}
}

func assertAppError(t *testing.T, err error, wantStatus int, wantMessage string) {
	t.Helper()

	var appErr *AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %T %v, want *AppError", err, err)
	}
	if appErr.Status != wantStatus {
		t.Errorf("status = %d, want %d", appErr.Status, wantStatus)
	}
	if appErr.Message != wantMessage {
		t.Errorf("message = %q, want %q", appErr.Message, wantMessage)
	}
}
