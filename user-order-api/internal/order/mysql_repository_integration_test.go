package order

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/user"
)

func TestMySQLRepositoryCreatesPendingOrder(t *testing.T) {
	db := openMySQLTestDatabase(t)
	userRepo := user.NewMySQLRepository(db)
	orderRepo := NewMySQLRepository(db)

	email := fmt.Sprintf("order-owner-%d@example.com", time.Now().UnixNano())
	createdUser, err := userRepo.Create(context.Background(), user.CreateUserRequest{Name: "Order Owner", Email: email})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM orders WHERE user_id = ?", createdUser.ID)
		_, _ = db.Exec("DELETE FROM users WHERE id = ?", createdUser.ID)
	})

	createdOrder, err := orderRepo.Create(context.Background(), CreateOrderRequest{UserID: createdUser.ID, Amount: 2599})
	if err != nil {
		t.Fatal(err)
	}
	if createdOrder.Status != StatusPending {
		t.Fatalf("Status = %q, want %q", createdOrder.Status, StatusPending)
	}
	if createdOrder.UserID != createdUser.ID || createdOrder.Amount != 2599 {
		t.Fatalf("unexpected order: %+v", createdOrder)
	}
}

func TestMySQLRepositoryMapsMissingRecordsAndUsers(t *testing.T) {
	db := openMySQLTestDatabase(t)
	repo := NewMySQLRepository(db)

	_, err := repo.FindByID(context.Background(), 9_999_999_999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindByID() error = %v, want ErrNotFound", err)
	}

	_, err = repo.Create(context.Background(), CreateOrderRequest{UserID: 9_999_999_999, Amount: 100})
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Create() error = %v, want ErrUserNotFound", err)
	}
}

func openMySQLTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set")
	}

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
