package app

import (
	"context"
	"errors"
	"strings"
	"sync"

	"bridge-go/user-order-api/internal/auth"
	"bridge-go/user-order-api/internal/user"
)

type memoryIdentityRepository struct {
	users   user.Repository
	mu      sync.RWMutex
	items   map[int64]auth.Identity
	byEmail map[string]int64
}

func newMemoryIdentityRepository(users user.Repository) *memoryIdentityRepository {
	return &memoryIdentityRepository{users: users, items: map[int64]auth.Identity{}, byEmail: map[string]int64{}}
}
func (r *memoryIdentityRepository) CreateIdentity(ctx context.Context, input auth.NewIdentity) (auth.Identity, error) {
	created, err := r.users.Create(ctx, user.CreateUserRequest{Name: input.Name, Email: input.Email})
	if err != nil {
		if errors.Is(err, user.ErrEmailTaken) {
			return auth.Identity{}, auth.ErrEmailTaken
		}
		return auth.Identity{}, err
	}
	role := input.Role
	if role == "" {
		role = auth.RoleUser
	}
	item := auth.Identity{ID: created.ID, Name: created.Name, Email: created.Email, PasswordHash: input.PasswordHash, Role: role, AuthVersion: 1, CreatedAt: created.CreatedAt}
	r.mu.Lock()
	r.items[item.ID] = item
	r.byEmail[strings.ToLower(item.Email)] = item.ID
	r.mu.Unlock()
	return item, nil
}
func (r *memoryIdentityRepository) FindIdentityByEmail(_ context.Context, email string) (auth.Identity, error) {
	r.mu.RLock()
	id, ok := r.byEmail[strings.ToLower(strings.TrimSpace(email))]
	item := r.items[id]
	r.mu.RUnlock()
	if !ok {
		return auth.Identity{}, auth.ErrIdentityNotFound
	}
	return item, nil
}
func (r *memoryIdentityRepository) FindIdentityByID(_ context.Context, id int64) (auth.Identity, error) {
	r.mu.RLock()
	item, ok := r.items[id]
	r.mu.RUnlock()
	if !ok {
		return auth.Identity{}, auth.ErrIdentityNotFound
	}
	return item, nil
}
