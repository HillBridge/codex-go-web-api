package order

import (
	"context"
	"errors"
	"testing"

	"bridge-go/user-order-api/internal/platform/page"
)

func TestMemoryRepositoryListPaginatesByID(t *testing.T) {
	repo := NewMemoryRepository()
	for userID := int64(1); userID <= 3; userID++ {
		if _, _, err := repo.Create(context.Background(), CreateOrderRequest{UserID: userID, Amount: 100}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := repo.List(context.Background(), page.Request{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != 1 || first.Items[1].ID != 2 || first.NextCursor != "2" {
		t.Fatalf("first page = %+v, want orders 1 and 2 with cursor 2", first)
	}

	second, err := repo.List(context.Background(), page.Request{Limit: 2, AfterID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != 3 || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want order 3 without cursor", second)
	}
}

func TestMemoryRepositoryReplaysMatchingIdempotencyKey(t *testing.T) {
	repo := NewMemoryRepository()
	input := CreateOrderRequest{UserID: 1, Amount: 2599, IdempotencyKey: "create-order-1"}

	first, replayed, err := repo.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first create was marked as replayed")
	}

	second, replayed, err := repo.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed {
		t.Fatal("matching idempotency key was not replayed")
	}
	if second.ID != first.ID {
		t.Fatalf("replayed order ID = %d, want %d", second.ID, first.ID)
	}
}

func TestMemoryRepositoryRejectsDifferentRequestForIdempotencyKey(t *testing.T) {
	repo := NewMemoryRepository()
	_, _, err := repo.Create(context.Background(), CreateOrderRequest{UserID: 1, Amount: 2599, IdempotencyKey: "create-order-1"})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = repo.Create(context.Background(), CreateOrderRequest{UserID: 1, Amount: 2600, IdempotencyKey: "create-order-1"})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("Create() error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestMemoryRepositoryTransitionsPendingOrderOnlyOnce(t *testing.T) {
	repo := NewMemoryRepository()
	created, _, err := repo.Create(context.Background(), CreateOrderRequest{UserID: 1, Amount: 2599})
	if err != nil {
		t.Fatal(err)
	}

	paid, changed, err := repo.Transition(context.Background(), created.ID, StatusPaid)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || paid.Status != StatusPaid {
		t.Fatalf("pay result = (%+v, changed=%t), want paid and changed", paid, changed)
	}

	repeated, changed, err := repo.Transition(context.Background(), created.ID, StatusPaid)
	if err != nil {
		t.Fatal(err)
	}
	if changed || repeated.Status != StatusPaid {
		t.Fatalf("repeated pay = (%+v, changed=%t), want unchanged paid order", repeated, changed)
	}

	_, _, err = repo.Transition(context.Background(), created.ID, StatusCancelled)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("cancel paid order error = %v, want ErrInvalidState", err)
	}
}
