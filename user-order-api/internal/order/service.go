package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/httpx"
	"bridge-go/user-order-api/internal/platform/outbox"
	"bridge-go/user-order-api/internal/platform/page"
	"bridge-go/user-order-api/internal/user"
)

type UserFinder interface {
	FindByID(ctx context.Context, id int64) (user.User, error)
}

type Service struct {
	repo  Repository
	users UserFinder
	audit audit.Logger
}

func NewService(repo Repository, users UserFinder, audit audit.Logger) *Service {
	return &Service{repo: repo, users: users, audit: audit}
}

func (s *Service) Create(ctx context.Context, input CreateOrderRequest) (Order, bool, error) {
	if input.UserID <= 0 {
		return Order{}, false, httpx.BadRequest("userId is required")
	}
	if input.Amount <= 0 {
		return Order{}, false, httpx.BadRequest("amount must be greater than 0")
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKeyProvided && input.IdempotencyKey == "" {
		return Order{}, false, httpx.BadRequest("Idempotency-Key must not be empty")
	}
	if len(input.IdempotencyKey) > 255 {
		return Order{}, false, httpx.BadRequest("Idempotency-Key must be at most 255 characters")
	}

	if _, err := s.users.FindByID(ctx, input.UserID); err != nil {
		if errors.Is(err, user.ErrNotFound) {
			return Order{}, false, httpx.BadRequestCode("USER_NOT_FOUND", "user does not exist")
		}
		return Order{}, false, httpx.Internal("failed to find user", fmt.Errorf("find user: %w", err))
	}

	var (
		item           Order
		replayed       bool
		eventPersisted bool
		err            error
	)
	if repo, ok := s.repo.(interface {
		CreateWithEvent(context.Context, CreateOrderRequest, func(Order) (outbox.Event, error)) (Order, bool, bool, error)
	}); ok {
		item, replayed, eventPersisted, err = repo.CreateWithEvent(ctx, input, func(order Order) (outbox.Event, error) {
			return outbox.NewEvent("order.created", "order", order.ID, map[string]any{"orderID": order.ID, "userID": order.UserID}, time.Now().UTC())
		})
	} else {
		item, replayed, err = s.repo.Create(ctx, input)
	}
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return Order{}, false, httpx.BadRequestCode("USER_NOT_FOUND", "user does not exist")
		}
		if errors.Is(err, ErrIdempotencyConflict) {
			return Order{}, false, httpx.ConflictCode("IDEMPOTENCY_KEY_CONFLICT", "Idempotency-Key conflicts with an existing order")
		}
		return Order{}, false, httpx.Internal("failed to create order", fmt.Errorf("create order: %w", err))
	}

	if !replayed && !eventPersisted {
		s.audit.Record(ctx, "order.created", map[string]any{"orderID": item.ID, "userID": item.UserID})
	}
	return item, replayed, nil
}

func (s *Service) Pay(ctx context.Context, id int64) (Order, error) {
	return s.transition(ctx, id, StatusPaid, "order.paid")
}

func (s *Service) Cancel(ctx context.Context, id int64) (Order, error) {
	return s.transition(ctx, id, StatusCancelled, "order.cancelled")
}

func (s *Service) transition(ctx context.Context, id int64, target Status, action string) (Order, error) {
	var (
		item           Order
		changed        bool
		eventPersisted bool
		err            error
	)
	if repo, ok := s.repo.(interface {
		TransitionWithEvent(context.Context, int64, Status, func(Order) (outbox.Event, error)) (Order, bool, bool, error)
	}); ok {
		item, changed, eventPersisted, err = repo.TransitionWithEvent(ctx, id, target, func(order Order) (outbox.Event, error) {
			return outbox.NewEvent(action, "order", order.ID, map[string]any{"orderID": order.ID, "userID": order.UserID}, time.Now().UTC())
		})
	} else {
		item, changed, err = s.repo.Transition(ctx, id, target)
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Order{}, httpx.NotFoundCode("ORDER_NOT_FOUND", "order not found")
		}
		if errors.Is(err, ErrInvalidState) {
			return Order{}, httpx.ConflictCode("INVALID_ORDER_STATE", "invalid order state transition")
		}
		return Order{}, httpx.Internal("failed to transition order", fmt.Errorf("transition order: %w", err))
	}
	if changed {
		if !eventPersisted {
			s.audit.Record(ctx, action, map[string]any{"orderID": item.ID, "userID": item.UserID})
		}
	}
	return item, nil
}

func (s *Service) List(ctx context.Context, request page.Request) (page.Result[Order], error) {
	orders, err := s.repo.List(ctx, request)
	if err != nil {
		return page.Result[Order]{}, httpx.Internal("failed to list orders", fmt.Errorf("list orders: %w", err))
	}
	return orders, nil
}

func (s *Service) ListByUserID(ctx context.Context, userID int64, request page.Request) (page.Result[Order], error) {
	orders, err := s.repo.ListByUserID(ctx, userID, request)
	if err != nil {
		return page.Result[Order]{}, httpx.Internal("failed to list orders", fmt.Errorf("list orders by user: %w", err))
	}
	return orders, nil
}

func (s *Service) FindByID(ctx context.Context, id int64) (Order, error) {
	order, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Order{}, httpx.NotFoundCode("ORDER_NOT_FOUND", "order not found")
		}
		return Order{}, httpx.Internal("failed to find order", fmt.Errorf("find order: %w", err))
	}
	return order, nil
}
