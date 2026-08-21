package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/platform/testdb"
	"bridge-go/user-order-api/internal/user"
)

func TestMySQLRepositoryStoresOnlyRefreshTokenHash(t *testing.T) {
	db := openMySQLTestDatabase(t)
	userRepo := user.NewMySQLRepository(db)
	createdUser, err := userRepo.Create(context.Background(), user.CreateUserRequest{Name: "Session Owner", Email: "session-owner-auth@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM sessions WHERE user_id = ?", createdUser.ID)
		_, _ = db.Exec("DELETE FROM users WHERE id = ?", createdUser.ID)
	})

	rawToken := "raw-refresh-token-must-not-be-persisted"
	digest := sha256.Sum256([]byte(rawToken))
	repo := NewMySQLRepository(db)
	created, err := repo.Create(context.Background(), NewSession{ID: "11111111-1111-1111-1111-111111111111", UserID: createdUser.ID, TokenHash: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if created.TokenHash == rawToken {
		t.Fatal("session exposed raw refresh token")
	}
	var stored string
	if err := db.QueryRow("SELECT token_hash FROM sessions WHERE id = ?", created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == rawToken || stored != created.TokenHash {
		t.Fatalf("stored token hash = %q, want hash only", stored)
	}
}

func openMySQLTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
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
	return db
}
