package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type MySQLRepository struct {
	db *sql.DB
}

func NewMySQLRepository(db *sql.DB) *MySQLRepository {
	return &MySQLRepository{db: db}
}

func (r *MySQLRepository) CreateIdentity(ctx context.Context, input NewIdentity) (Identity, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	role := input.Role
	if role == "" {
		role = RoleUser
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO users (name, email, password_hash, role, created_at)
		VALUES (?, ?, ?, ?, UTC_TIMESTAMP(6))`, strings.TrimSpace(input.Name), email, input.PasswordHash, role)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return Identity{}, ErrEmailTaken
		}
		return Identity{}, fmt.Errorf("insert identity: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Identity{}, fmt.Errorf("read identity ID: %w", err)
	}
	return r.FindIdentityByID(ctx, id)
}

func (r *MySQLRepository) FindIdentityByEmail(ctx context.Context, email string) (Identity, error) {
	item, err := scanIdentity(r.db.QueryRowContext(ctx, `
		SELECT id, name, email, password_hash, role, auth_version, created_at
		FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))))
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrIdentityNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("find identity by email: %w", err)
	}
	return item, nil
}

func (r *MySQLRepository) FindIdentityByID(ctx context.Context, id int64) (Identity, error) {
	item, err := scanIdentity(r.db.QueryRowContext(ctx, `
		SELECT id, name, email, password_hash, role, auth_version, created_at
		FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrIdentityNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("find identity by ID: %w", err)
	}
	return item, nil
}

func (r *MySQLRepository) Create(ctx context.Context, input NewSession) (Session, error) {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at, last_used_at)
		VALUES (?, ?, ?, ?, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`,
		input.ID, input.UserID, input.TokenHash, input.ExpiresAt.UTC(),
	); err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	return r.findByID(ctx, input.ID)
}

func (r *MySQLRepository) FindActiveByTokenHash(ctx context.Context, hash string, now time.Time) (Session, error) {
	item, err := scanSession(r.db.QueryRowContext(ctx, `
		SELECT id, user_id, token_hash, expires_at, revoked_at, created_at, last_used_at
		FROM sessions
		WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?`, hash, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("find active session: %w", err)
	}
	return item, nil
}

func (r *MySQLRepository) FindActiveByID(ctx context.Context, id string, now time.Time) (Session, error) {
	item, err := scanSession(r.db.QueryRowContext(ctx, `
		SELECT id, user_id, token_hash, expires_at, revoked_at, created_at, last_used_at
		FROM sessions WHERE id = ? AND revoked_at IS NULL AND expires_at > ?`, id, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("find active session by ID: %w", err)
	}
	return item, nil
}

func (r *MySQLRepository) Rotate(ctx context.Context, id string, replacement NewSession) (Session, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin session rotation: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = UTC_TIMESTAMP(6), last_used_at = UTC_TIMESTAMP(6)
		WHERE id = ? AND revoked_at IS NULL AND expires_at > UTC_TIMESTAMP(6)`, id)
	if err != nil {
		return Session{}, fmt.Errorf("revoke old session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("read session rotation result: %w", err)
	}
	if updated != 1 {
		return Session{}, ErrSessionNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at, last_used_at)
		VALUES (?, ?, ?, ?, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`,
		replacement.ID, replacement.UserID, replacement.TokenHash, replacement.ExpiresAt.UTC(),
	); err != nil {
		return Session{}, fmt.Errorf("insert replacement session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit session rotation: %w", err)
	}
	return r.findByID(ctx, replacement.ID)
}

func (r *MySQLRepository) Revoke(ctx context.Context, id string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = ?, last_used_at = ? WHERE id = ? AND revoked_at IS NULL`, now.UTC(), now.UTC(), id)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revoke result: %w", err)
	}
	if updated != 1 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *MySQLRepository) findByID(ctx context.Context, id string) (Session, error) {
	item, err := scanSession(r.db.QueryRowContext(ctx, `
		SELECT id, user_id, token_hash, expires_at, revoked_at, created_at, last_used_at
		FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("find session: %w", err)
	}
	return item, nil
}

type sessionScanner interface{ Scan(...any) error }

type identityScanner interface{ Scan(...any) error }

func scanIdentity(scanner identityScanner) (Identity, error) {
	var item Identity
	var role string
	if err := scanner.Scan(&item.ID, &item.Name, &item.Email, &item.PasswordHash, &role, &item.AuthVersion, &item.CreatedAt); err != nil {
		return Identity{}, err
	}
	item.Role = Role(role)
	return item, nil
}

func scanSession(scanner sessionScanner) (Session, error) {
	var item Session
	var revoked sql.NullTime
	if err := scanner.Scan(&item.ID, &item.UserID, &item.TokenHash, &item.ExpiresAt, &revoked, &item.CreatedAt, &item.LastUsedAt); err != nil {
		return Session{}, err
	}
	if revoked.Valid {
		item.RevokedAt = &revoked.Time
	}
	return item, nil
}
