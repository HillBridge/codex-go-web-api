package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrSessionNotFound = errors.New("session not found")

type Repository interface {
	Create(context.Context, NewSession) (Session, error)
	FindActiveByTokenHash(context.Context, string, time.Time) (Session, error)
	FindActiveByID(context.Context, string, time.Time) (Session, error)
	Rotate(context.Context, string, NewSession) (Session, error)
	Revoke(context.Context, string, time.Time) error
}

type MemoryRepository struct {
	mu       sync.RWMutex
	sessions map[string]Session
	byHash   map[string]string
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{sessions: make(map[string]Session), byHash: make(map[string]string)}
}

func (r *MemoryRepository) Create(ctx context.Context, input NewSession) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	item := Session{ID: input.ID, UserID: input.UserID, TokenHash: input.TokenHash, ExpiresAt: input.ExpiresAt, CreatedAt: now, LastUsedAt: now}
	r.sessions[item.ID] = item
	r.byHash[item.TokenHash] = item.ID
	return item, nil
}

func (r *MemoryRepository) FindActiveByTokenHash(ctx context.Context, hash string, now time.Time) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, exists := r.byHash[hash]
	if !exists {
		return Session{}, ErrSessionNotFound
	}
	item := r.sessions[id]
	if item.RevokedAt != nil || !item.ExpiresAt.After(now) {
		return Session{}, ErrSessionNotFound
	}
	return item, nil
}

func (r *MemoryRepository) FindActiveByID(ctx context.Context, id string, now time.Time) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, exists := r.sessions[id]
	if !exists || item.RevokedAt != nil || !item.ExpiresAt.After(now) {
		return Session{}, ErrSessionNotFound
	}
	return item, nil
}

func (r *MemoryRepository) Rotate(ctx context.Context, id string, replacement NewSession) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.sessions[id]
	if !exists || item.RevokedAt != nil || !item.ExpiresAt.After(time.Now().UTC()) {
		return Session{}, ErrSessionNotFound
	}
	now := time.Now().UTC()
	item.RevokedAt = &now
	r.sessions[id] = item
	created := Session{ID: replacement.ID, UserID: replacement.UserID, TokenHash: replacement.TokenHash, ExpiresAt: replacement.ExpiresAt, CreatedAt: now, LastUsedAt: now}
	r.sessions[created.ID] = created
	r.byHash[created.TokenHash] = created.ID
	return created, nil
}

func (r *MemoryRepository) Revoke(ctx context.Context, id string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.sessions[id]
	if !exists || item.RevokedAt != nil {
		return ErrSessionNotFound
	}
	item.RevokedAt = &now
	r.sessions[id] = item
	return nil
}
