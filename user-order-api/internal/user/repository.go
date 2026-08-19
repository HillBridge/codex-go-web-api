package user

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"bridge-go/user-order-api/internal/platform/page"
)

var ErrNotFound = errors.New("user not found")
var ErrEmailTaken = errors.New("email already exists")

type Repository interface {
	Create(ctx context.Context, input CreateUserRequest) (User, error)
	List(ctx context.Context, request page.Request) (page.Result[User], error)
	FindByID(ctx context.Context, id int64) (User, error)
}

type MemoryRepository struct {
	mu      sync.RWMutex
	nextID  int64
	users   map[int64]User
	byEmail map[string]int64
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		nextID:  1,
		users:   make(map[int64]User),
		byEmail: make(map[string]int64),
	}
}

func (r *MemoryRepository) Create(ctx context.Context, input CreateUserRequest) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	email := strings.ToLower(strings.TrimSpace(input.Email))
	if _, exists := r.byEmail[email]; exists {
		return User{}, ErrEmailTaken
	}

	user := User{
		ID:        r.nextID,
		Name:      strings.TrimSpace(input.Name),
		Email:     email,
		CreatedAt: time.Now().UTC(),
	}
	r.nextID++
	r.users[user.ID] = user
	r.byEmail[user.Email] = user.ID

	return user, nil
}

func (r *MemoryRepository) List(ctx context.Context, request page.Request) (page.Result[User], error) {
	if err := ctx.Err(); err != nil {
		return page.Result[User]{}, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]User, 0, len(r.users))
	for _, item := range r.users {
		if item.ID > request.AfterID {
			users = append(users, item)
		}
	}
	slices.SortFunc(users, func(a, b User) int {
		return int(a.ID - b.ID)
	})

	return paginate(users, request.Limit), nil
}

func paginate(items []User, limit int) page.Result[User] {
	if len(items) <= limit {
		return page.Result[User]{Items: items}
	}
	items = items[:limit]
	return page.Result[User]{Items: items, NextCursor: strconv.FormatInt(items[len(items)-1].ID, 10)}
}

func (r *MemoryRepository) FindByID(ctx context.Context, id int64) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	user, exists := r.users[id]
	if !exists {
		return User{}, ErrNotFound
	}

	return user, nil
}
