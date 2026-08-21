package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryRepositoryRotatesRefreshTokenOnlyOnce(t *testing.T) {
	repo := NewMemoryRepository()
	created, err := repo.Create(context.Background(), NewSession{ID: "session-1", UserID: 1, TokenHash: "old-hash", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	rotated, err := repo.Rotate(context.Background(), created.ID, NewSession{ID: "session-2", UserID: created.UserID, TokenHash: "new-hash", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.TokenHash != "new-hash" || rotated.UserID != created.UserID {
		t.Fatalf("rotated session = %+v", rotated)
	}

	_, err = repo.Rotate(context.Background(), created.ID, NewSession{ID: "session-3", UserID: created.UserID, TokenHash: "another-hash", ExpiresAt: time.Now().Add(time.Hour)})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second Rotate() error = %v, want ErrSessionNotFound", err)
	}
}
