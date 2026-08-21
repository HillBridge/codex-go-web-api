package auth

import (
	"strings"
	"testing"
	"time"
)

func TestTokenManagerRejectsExpiredAndTamperedTokens(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	manager := NewTokenManager([]byte("test-signing-key-with-at-least-32-bytes"), "user-order-api", 15*time.Minute, func() time.Time { return now })
	token, err := manager.Issue(Principal{UserID: 7, Role: "user", SessionID: "session-1", AuthVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := manager.Parse(token)
	if err != nil || principal.UserID != 7 || principal.Role != "user" {
		t.Fatalf("Parse() = (%+v, %v), want user principal", principal, err)
	}

	tampered := token[:len(token)-1] + "x"
	if _, err := manager.Parse(tampered); err == nil {
		t.Fatal("Parse(tampered) error = nil, want signature error")
	}

	expired := NewTokenManager([]byte("test-signing-key-with-at-least-32-bytes"), "user-order-api", time.Minute, func() time.Time { return now.Add(-2 * time.Minute) })
	expiredToken, err := expired.Issue(Principal{UserID: 7, Role: "user", SessionID: "session-1", AuthVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Parse(expiredToken); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Parse(expired) error = %v, want expiration error", err)
	}
}
