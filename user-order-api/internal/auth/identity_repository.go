package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrIdentityNotFound = errors.New("identity not found")
	ErrEmailTaken       = errors.New("identity email already exists")
)

type IdentityRepository interface {
	CreateIdentity(context.Context, NewIdentity) (Identity, error)
	FindIdentityByEmail(context.Context, string) (Identity, error)
	FindIdentityByID(context.Context, int64) (Identity, error)
}

type MemoryIdentityRepository struct {
	mu      sync.RWMutex
	nextID  int64
	byID    map[int64]Identity
	byEmail map[string]int64
}

func NewMemoryIdentityRepository() *MemoryIdentityRepository {
	return &MemoryIdentityRepository{nextID: 1, byID: make(map[int64]Identity), byEmail: make(map[string]int64)}
}

func (r *MemoryIdentityRepository) CreateIdentity(ctx context.Context, input NewIdentity) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if _, exists := r.byEmail[email]; exists {
		return Identity{}, ErrEmailTaken
	}
	role := input.Role
	if role == "" {
		role = RoleUser
	}
	item := Identity{ID: r.nextID, Name: strings.TrimSpace(input.Name), Email: email, PasswordHash: input.PasswordHash, Role: role, AuthVersion: 1, CreatedAt: time.Now().UTC()}
	r.nextID++
	r.byID[item.ID] = item
	r.byEmail[item.Email] = item.ID
	return item, nil
}

func (r *MemoryIdentityRepository) FindIdentityByEmail(ctx context.Context, email string) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, exists := r.byEmail[strings.ToLower(strings.TrimSpace(email))]
	if !exists {
		return Identity{}, ErrIdentityNotFound
	}
	return r.byID[id], nil
}

func (r *MemoryIdentityRepository) FindIdentityByID(ctx context.Context, id int64) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, exists := r.byID[id]
	if !exists {
		return Identity{}, ErrIdentityNotFound
	}
	return item, nil
}
