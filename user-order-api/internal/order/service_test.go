package order

import (
	"context"
	"errors"
	"testing"

	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/user"
)

func TestServiceRejectsTransitionAcrossTerminalStates(t *testing.T) {
	users := user.NewMemoryRepository()
	createdUser, err := users.Create(context.Background(), user.CreateUserRequest{Name: "Ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryRepository(), users, discardAuditLogger{})
	createdOrder, _, err := service.Create(context.Background(), CreateOrderRequest{UserID: createdUser.ID, Amount: 2599})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pay(context.Background(), createdOrder.ID); err != nil {
		t.Fatal(err)
	}

	_, err = service.Cancel(context.Background(), createdOrder.ID)
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("Cancel() error = %v, want AppError", err)
	}
	if appErr.Status != 409 || appErr.Code != "INVALID_ORDER_STATE" {
		t.Fatalf("Cancel() error = %+v, want 409 INVALID_ORDER_STATE", appErr)
	}
}

type discardAuditLogger struct{}

func (discardAuditLogger) Record(context.Context, string, map[string]any) {}
