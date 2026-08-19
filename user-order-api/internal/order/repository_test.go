package order

import (
	"context"
	"testing"

	"bridge-go/user-order-api/internal/platform/page"
)

func TestMemoryRepositoryListPaginatesByID(t *testing.T) {
	repo := NewMemoryRepository()
	for userID := int64(1); userID <= 3; userID++ {
		if _, err := repo.Create(context.Background(), CreateOrderRequest{UserID: userID, Amount: 100}); err != nil {
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
