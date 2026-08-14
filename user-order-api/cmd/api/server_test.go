package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserAndOrderFlow(t *testing.T) {
	server := newServer()

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
	server := newServer()

	postJSON(t, server, "/orders", map[string]any{
		"userId": 99,
		"amount": 100,
	}, http.StatusBadRequest)
}

func TestDuplicateEmailIsRejected(t *testing.T) {
	server := newServer()

	payload := map[string]any{
		"name":  "Ada",
		"email": "ada@example.com",
	}
	postJSON(t, server, "/users", payload, http.StatusCreated)
	postJSON(t, server, "/users", payload, http.StatusBadRequest)
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
