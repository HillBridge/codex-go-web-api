package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/platform/httpx"
)

func TestServiceLoginReturnsSameErrorForMissingUserAndWrongPassword(t *testing.T) {
	service := newTestService(t)
	if _, err := service.Register(context.Background(), RegisterRequest{Name: "Ada", Email: "ada@example.com", Password: "correct-password"}); err != nil {
		t.Fatal(err)
	}

	for _, input := range []LoginRequest{{Email: "missing@example.com", Password: "correct-password"}, {Email: "ada@example.com", Password: "wrong-password"}} {
		_, err := service.Login(context.Background(), input)
		var appErr *httpx.AppError
		if !errors.As(err, &appErr) || appErr.Status != 401 || appErr.Code != "INVALID_CREDENTIALS" {
			t.Fatalf("Login(%q) error = %v, want 401 INVALID_CREDENTIALS", input.Email, err)
		}
	}
}

func TestServiceRefreshRotatesTokenAndRejectsReuse(t *testing.T) {
	service := newTestService(t)
	created, err := service.Register(context.Background(), RegisterRequest{Name: "Ada", Email: "ada@example.com", Password: "correct-password"})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := service.Refresh(context.Background(), created.RefreshToken)
	if err != nil || refreshed.RefreshToken == created.RefreshToken {
		t.Fatalf("Refresh() = (%+v, %v), want a new token", refreshed, err)
	}
	_, err = service.Refresh(context.Background(), created.RefreshToken)
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) || appErr.Status != 401 || appErr.Code != "UNAUTHENTICATED" {
		t.Fatalf("reused Refresh() error = %v, want 401 UNAUTHENTICATED", err)
	}
}

func TestBootstrapAdminCreatesOnlyMissingConfiguredAccount(t *testing.T) {
	service := newTestService(t)
	created, err := service.BootstrapAdmin(context.Background(), "admin@example.com", "correct-password")
	if err != nil || !created {
		t.Fatalf("first BootstrapAdmin() = (%t, %v), want created", created, err)
	}
	identity, err := service.identities.FindIdentityByEmail(context.Background(), "admin@example.com")
	if err != nil || identity.Role != RoleAdmin || identity.PasswordHash == "" {
		t.Fatalf("bootstrap identity = (%+v, %v), want password-protected admin", identity, err)
	}
	created, err = service.BootstrapAdmin(context.Background(), "admin@example.com", "correct-password")
	if err != nil || created {
		t.Fatalf("second BootstrapAdmin() = (%t, %v), want no duplicate", created, err)
	}
}

func TestServiceAuditsRegistrationWithoutCredentials(t *testing.T) {
	service := newTestService(t)
	recorder := &auditRecorder{}
	service.SetAuditLogger(recorder)
	if _, err := service.Register(context.Background(), RegisterRequest{Name: "Ada", Email: "ada@example.com", Password: "correct-password"}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 1 || recorder.events[0].action != "auth.registered" || recorder.events[0].fields["userID"] == nil {
		t.Fatalf("audit events = %+v", recorder.events)
	}
	for key := range recorder.events[0].fields {
		if key == "password" || key == "token" || key == "passwordHash" {
			t.Fatalf("sensitive audit field %q", key)
		}
	}
}

func TestServiceAuditsLoginRefreshAndLogout(t *testing.T) {
	service := newTestService(t)
	recorder := &auditRecorder{}
	service.SetAuditLogger(recorder)
	if _, err := service.Register(context.Background(), RegisterRequest{Name: "Ada", Email: "ada@example.com", Password: "correct-password"}); err != nil {
		t.Fatal(err)
	}
	recorder.events = nil
	loggedIn, err := service.Login(context.Background(), LoginRequest{Email: "ada@example.com", Password: "correct-password"})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := service.Refresh(context.Background(), loggedIn.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(context.Background(), refreshed.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 3 || recorder.events[0].action != "auth.logged_in" || recorder.events[1].action != "auth.refreshed" || recorder.events[2].action != "auth.logged_out" {
		t.Fatalf("audit events = %+v", recorder.events)
	}
}

type auditEvent struct {
	action string
	fields map[string]any
}

type auditRecorder struct{ events []auditEvent }

func (r *auditRecorder) Record(_ context.Context, action string, fields map[string]any) {
	r.events = append(r.events, auditEvent{action: action, fields: fields})
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	now := time.Now().UTC()
	return NewService(NewMemoryIdentityRepository(), NewMemoryRepository(), NewTokenManager([]byte("test-signing-key-with-at-least-32-bytes"), "user-order-api", 15*time.Minute, func() time.Time { return now }), time.Hour, func() time.Time { return now })
}
