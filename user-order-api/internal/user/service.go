package user

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
)

type Service struct {
	repo  Repository
	audit audit.Logger
}

func NewService(repo Repository, audit audit.Logger) *Service {
	return &Service{repo: repo, audit: audit}
}

func (s *Service) Create(ctx context.Context, input CreateUserRequest) (User, error) {
	if strings.TrimSpace(input.Name) == "" {
		return User{}, httpx.BadRequest("name is required")
	}
	if !strings.Contains(input.Email, "@") {
		return User{}, httpx.BadRequest("valid email is required")
	}

	var (
		user           User
		err            error
		eventPersisted bool
	)
	if repo, ok := s.repo.(interface {
		CreateWithEvent(context.Context, CreateUserRequest, func(User) (outbox.Event, error)) (User, bool, error)
	}); ok {
		user, eventPersisted, err = repo.CreateWithEvent(ctx, input, func(item User) (outbox.Event, error) {
			return outbox.NewEvent("user.created", "user", item.ID, map[string]any{"userID": item.ID}, time.Now().UTC())
		})
	} else {
		user, err = s.repo.Create(ctx, input)
	}
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return User{}, httpx.BadRequestCode("EMAIL_ALREADY_EXISTS", "email already exists")
		}
		return User{}, httpx.Internal("failed to create user", fmt.Errorf("create user: %w", err))
	}

	if !eventPersisted {
		s.audit.Record(ctx, "user.created", map[string]any{"userID": user.ID})
	}
	return user, nil
}

func (s *Service) List(ctx context.Context, request page.Request) (page.Result[User], error) {
	users, err := s.repo.List(ctx, request)
	if err != nil {
		return page.Result[User]{}, httpx.Internal("failed to list users", fmt.Errorf("list users: %w", err))
	}
	return users, nil
}

func (s *Service) FindByID(ctx context.Context, id int64) (User, error) {
	user, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, httpx.NotFoundCode("USER_NOT_FOUND", "user not found")
		}
		return User{}, httpx.Internal("failed to find user", fmt.Errorf("find user: %w", err))
	}
	return user, nil
}
