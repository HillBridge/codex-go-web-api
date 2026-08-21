package auth

import "time"

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type Identity struct {
	ID           int64
	Name         string
	Email        string
	PasswordHash string
	Role         Role
	AuthVersion  int64
	CreatedAt    time.Time
}

type NewIdentity struct {
	Name         string
	Email        string
	PasswordHash string
	Role         Role
}

type Session struct {
	ID         string
	UserID     int64
	TokenHash  string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	LastUsedAt time.Time
}

type NewSession struct {
	ID        string
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
}
