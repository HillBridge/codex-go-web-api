package user

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("user not found")
var ErrEmailTaken = errors.New("email already exists")

type Repository interface {
	Create(ctx context.Context, input CreateUserRequest) (User, error)
	List(ctx context.Context) ([]User, error)
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

func (r *MemoryRepository) List(ctx context.Context) ([]User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]User, 0, len(r.users))
	for _, item := range r.users {
		users = append(users, item)
	}
	slices.SortFunc(users, func(a, b User) int {
		return int(a.ID - b.ID)
	})

	return users, nil
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
