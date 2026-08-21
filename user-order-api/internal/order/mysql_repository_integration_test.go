package order

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/platform/page"
	"bridge-go/user-order-api/internal/platform/testdb"
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

	createdOrder, _, err := orderRepo.Create(context.Background(), CreateOrderRequest{UserID: createdUser.ID, Amount: 2599})
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

	_, _, err = repo.Create(context.Background(), CreateOrderRequest{UserID: 9_999_999_999, Amount: 100})
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Create() error = %v, want ErrUserNotFound", err)
	}
}

func TestMySQLRepositoryReplaysMatchingIdempotencyKeyConcurrently(t *testing.T) {
	db := openMySQLTestDatabase(t)
	userRepo := user.NewMySQLRepository(db)
	orderRepo := NewMySQLRepository(db)
	createdUser := createMySQLOrderUser(t, userRepo)
	cleanupMySQLOrderUser(t, db, createdUser.ID)

	input := CreateOrderRequest{UserID: createdUser.ID, Amount: 2599, IdempotencyKey: fmt.Sprintf("order-%d", time.Now().UnixNano())}
	type result struct {
		order    Order
		replayed bool
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, replayed, err := orderRepo.Create(context.Background(), input)
			results <- result{order: item, replayed: replayed, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var orders []Order
	var replayCount int
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		orders = append(orders, result.order)
		if result.replayed {
			replayCount++
		}
	}
	if len(orders) != 2 || orders[0].ID != orders[1].ID || replayCount != 1 {
		t.Fatalf("concurrent results = %+v, replayCount=%d; want one created order and one replay", orders, replayCount)
	}
}

func TestMySQLRepositoryRejectsDifferentRequestForIdempotencyKey(t *testing.T) {
	db := openMySQLTestDatabase(t)
	userRepo := user.NewMySQLRepository(db)
	orderRepo := NewMySQLRepository(db)
	createdUser := createMySQLOrderUser(t, userRepo)
	cleanupMySQLOrderUser(t, db, createdUser.ID)

	key := fmt.Sprintf("order-%d", time.Now().UnixNano())
	if _, _, err := orderRepo.Create(context.Background(), CreateOrderRequest{UserID: createdUser.ID, Amount: 2599, IdempotencyKey: key}); err != nil {
		t.Fatal(err)
	}
	_, _, err := orderRepo.Create(context.Background(), CreateOrderRequest{UserID: createdUser.ID, Amount: 2600, IdempotencyKey: key})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("Create() error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestMySQLRepositoryPersistsAllowedStateTransitions(t *testing.T) {
	db := openMySQLTestDatabase(t)
	userRepo := user.NewMySQLRepository(db)
	orderRepo := NewMySQLRepository(db)
	createdUser := createMySQLOrderUser(t, userRepo)
	cleanupMySQLOrderUser(t, db, createdUser.ID)
	createdOrder, _, err := orderRepo.Create(context.Background(), CreateOrderRequest{UserID: createdUser.ID, Amount: 2599})
	if err != nil {
		t.Fatal(err)
	}

	paid, changed, err := orderRepo.Transition(context.Background(), createdOrder.ID, StatusPaid)
	if err != nil || !changed || paid.Status != StatusPaid {
		t.Fatalf("pay result = (%+v, %t, %v), want changed paid order", paid, changed, err)
	}
	_, changed, err = orderRepo.Transition(context.Background(), createdOrder.ID, StatusPaid)
	if err != nil || changed {
		t.Fatalf("repeated pay result = (changed=%t, err=%v), want unchanged success", changed, err)
	}
	_, _, err = orderRepo.Transition(context.Background(), createdOrder.ID, StatusCancelled)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("cancel paid order error = %v, want ErrInvalidState", err)
	}
}

func TestMySQLRepositoryListsOnlyRequestedUsersOrders(t *testing.T) {
	db := openMySQLTestDatabase(t)
	userRepo := user.NewMySQLRepository(db)
	orderRepo := NewMySQLRepository(db)
	firstUser := createMySQLOrderUser(t, userRepo)
	secondUser := createMySQLOrderUser(t, userRepo)
	cleanupMySQLOrderUser(t, db, firstUser.ID)
	cleanupMySQLOrderUser(t, db, secondUser.ID)
	if _, _, err := orderRepo.Create(context.Background(), CreateOrderRequest{UserID: firstUser.ID, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := orderRepo.Create(context.Background(), CreateOrderRequest{UserID: secondUser.ID, Amount: 200}); err != nil {
		t.Fatal(err)
	}

	result, err := orderRepo.ListByUserID(context.Background(), firstUser.ID, page.Request{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].UserID != firstUser.ID {
		t.Fatalf("scoped orders = %+v", result.Items)
	}
}

func createMySQLOrderUser(t *testing.T, repo user.Repository) user.User {
	t.Helper()
	created, err := repo.Create(context.Background(), user.CreateUserRequest{Name: "Order Owner", Email: fmt.Sprintf("order-owner-%d@example.com", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func cleanupMySQLOrderUser(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM orders WHERE user_id = ?", userID)
		_, _ = db.Exec("DELETE FROM users WHERE id = ?", userID)
	})
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
