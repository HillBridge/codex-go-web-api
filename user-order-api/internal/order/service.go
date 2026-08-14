package order

import (
	"context"
	"errors"
	"fmt"

	"bridge-go/user-order-api/internal/platform/audit"
	"bridge-go/user-order-api/internal/platform/httpx"
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

func (s *Service) Create(ctx context.Context, input CreateOrderRequest) (Order, error) {
	if input.UserID <= 0 {
		return Order{}, httpx.BadRequest("userId is required")
	}
	if input.Amount <= 0 {
		return Order{}, httpx.BadRequest("amount must be greater than 0")
	}

	if _, err := s.users.FindByID(ctx, input.UserID); err != nil {
		return Order{}, httpx.BadRequest("user does not exist")
	}

	order, err := s.repo.Create(ctx, input)
	if err != nil {
		return Order{}, httpx.Internal("failed to create order", fmt.Errorf("create order: %w", err))
	}

	s.audit.Record(ctx, "order.created", map[string]any{"orderID": order.ID, "userID": order.UserID})
	return order, nil
}

func (s *Service) List(ctx context.Context) ([]Order, error) {
	orders, err := s.repo.List(ctx)
	if err != nil {
		return nil, httpx.Internal("failed to list orders", fmt.Errorf("list orders: %w", err))
	}
	return orders, nil
}

func (s *Service) FindByID(ctx context.Context, id int64) (Order, error) {
	order, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Order{}, httpx.NotFound("order not found")
		}
		return Order{}, httpx.Internal("failed to find order", fmt.Errorf("find order: %w", err))
	}
	return order, nil
}
