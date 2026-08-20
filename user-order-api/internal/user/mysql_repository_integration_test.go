package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/platform/testdb"
)

func TestMySQLRepositoryCreatesNormalizedUserAndRejectsDuplicateEmail(t *testing.T) {
	db := openMySQLTestDatabase(t)
	repo := NewMySQLRepository(db)

	email := fmt.Sprintf("ada-%d@example.com", time.Now().UnixNano())
	created, err := repo.Create(context.Background(), CreateUserRequest{Name: " Ada ", Email: "  " + email + "  "})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM users WHERE id = ?", created.ID)
	})
	if created.Email != email {
		t.Fatalf("Email = %q, want normalized %q", created.Email, email)
	}

	_, err = repo.Create(context.Background(), CreateUserRequest{Name: "Other Ada", Email: email})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate email error = %v, want ErrEmailTaken", err)
	}
}

func TestMySQLRepositoryFindByIDMapsMissingUser(t *testing.T) {
	db := openMySQLTestDatabase(t)
	repo := NewMySQLRepository(db)

	_, err := repo.FindByID(context.Background(), 9_999_999_999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindByID() error = %v, want ErrNotFound", err)
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
