package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"bridge-go/user-order-api/internal/platform/outbox"
)

type MySQLRepository struct {
	db     *sql.DB
	events outbox.Repository
}

func NewMySQLRepository(db *sql.DB, events ...outbox.Repository) *MySQLRepository {
	var eventRepo outbox.Repository
	if len(events) > 0 {
		eventRepo = events[0]
	}
	return &MySQLRepository{db: db, events: eventRepo}
}

func (r *MySQLRepository) RegisterWithEvent(ctx context.Context, input NewIdentity, session NewSession, factory func(Identity, Session) (outbox.Event, error)) (Identity, Session, bool, error) {
	if r.events == nil {
		identity, err := r.CreateIdentity(ctx, input)
		if err != nil {
			return Identity{}, Session{}, false, err
		}
		created, err := r.Create(ctx, session)
		return identity, created, false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, Session{}, false, fmt.Errorf("begin registration: %w", err)
	}
	defer tx.Rollback()
	email := strings.ToLower(strings.TrimSpace(input.Email))
	role := input.Role
	if role == "" {
		role = RoleUser
	}
	createdAt := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO users (name, email, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`, strings.TrimSpace(input.Name), email, input.PasswordHash, role, createdAt)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return Identity{}, Session{}, false, ErrEmailTaken
		}
		return Identity{}, Session{}, false, fmt.Errorf("insert identity: %w", err)
	}
	identityID, err := result.LastInsertId()
	if err != nil {
		return Identity{}, Session{}, false, fmt.Errorf("read identity ID: %w", err)
	}
	identity := Identity{ID: identityID, Name: strings.TrimSpace(input.Name), Email: email, PasswordHash: input.PasswordHash, Role: role, AuthVersion: 1, CreatedAt: createdAt}
	sessionUserID := session.UserID
	if sessionUserID == 0 {
		sessionUserID = identityID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`, session.ID, sessionUserID, session.TokenHash, session.ExpiresAt.UTC(), createdAt, createdAt); err != nil {
		return Identity{}, Session{}, false, fmt.Errorf("insert registration session: %w", err)
	}
	createdSession := Session{ID: session.ID, UserID: sessionUserID, TokenHash: session.TokenHash, ExpiresAt: session.ExpiresAt, CreatedAt: createdAt, LastUsedAt: createdAt}
	event, err := factory(identity, createdSession)
	if err != nil {
		return Identity{}, Session{}, false, fmt.Errorf("build registration event: %w", err)
	}
	if err := r.events.AppendTx(ctx, tx, event); err != nil {
		return Identity{}, Session{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, Session{}, false, fmt.Errorf("commit registration: %w", err)
	}
	return identity, createdSession, true, nil
}

func (r *MySQLRepository) CreateSessionWithEvent(ctx context.Context, input NewSession, factory func(Session) (outbox.Event, error)) (Session, bool, error) {
	if r.events == nil {
		item, err := r.Create(ctx, input)
		return item, false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, false, fmt.Errorf("begin session creation: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`, input.ID, input.UserID, input.TokenHash, input.ExpiresAt.UTC(), now, now); err != nil {
		return Session{}, false, fmt.Errorf("insert session: %w", err)
	}
	item := Session{ID: input.ID, UserID: input.UserID, TokenHash: input.TokenHash, ExpiresAt: input.ExpiresAt, CreatedAt: now, LastUsedAt: now}
	event, err := factory(item)
	if err != nil {
		return Session{}, false, fmt.Errorf("build session event: %w", err)
	}
	if err := r.events.AppendTx(ctx, tx, event); err != nil {
		return Session{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, false, fmt.Errorf("commit session creation: %w", err)
	}
	return item, true, nil
}

func (r *MySQLRepository) RotateWithEvent(ctx context.Context, id string, replacement NewSession, factory func(Session) (outbox.Event, error)) (Session, bool, error) {
	if r.events == nil {
		item, err := r.Rotate(ctx, id, replacement)
		return item, false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, false, fmt.Errorf("begin session rotation: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = UTC_TIMESTAMP(6), last_used_at = UTC_TIMESTAMP(6) WHERE id = ? AND revoked_at IS NULL AND expires_at > UTC_TIMESTAMP(6)`, id)
	if err != nil {
		return Session{}, false, fmt.Errorf("revoke old session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Session{}, false, fmt.Errorf("read session rotation result: %w", err)
	}
	if updated != 1 {
		return Session{}, false, ErrSessionNotFound
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`, replacement.ID, replacement.UserID, replacement.TokenHash, replacement.ExpiresAt.UTC(), now, now); err != nil {
		return Session{}, false, fmt.Errorf("insert replacement session: %w", err)
	}
	item := Session{ID: replacement.ID, UserID: replacement.UserID, TokenHash: replacement.TokenHash, ExpiresAt: replacement.ExpiresAt, CreatedAt: now, LastUsedAt: now}
	event, err := factory(item)
	if err != nil {
		return Session{}, false, fmt.Errorf("build refresh event: %w", err)
	}
	if err := r.events.AppendTx(ctx, tx, event); err != nil {
		return Session{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, false, fmt.Errorf("commit session rotation: %w", err)
	}
	return item, true, nil
}

func (r *MySQLRepository) RevokeWithEvent(ctx context.Context, id string, now time.Time, factory func(Session) (outbox.Event, error)) (bool, error) {
	if r.events == nil {
		if err := r.Revoke(ctx, id, now); err != nil {
			return false, err
		}
		return false, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin session revoke: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ?, last_used_at = ? WHERE id = ? AND revoked_at IS NULL`, now.UTC(), now.UTC(), id)
	if err != nil {
		return false, fmt.Errorf("revoke session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read revoke result: %w", err)
	}
	if updated != 1 {
		return false, ErrSessionNotFound
	}
	item, err := scanSession(tx.QueryRowContext(ctx, `SELECT id, user_id, token_hash, expires_at, revoked_at, created_at, last_used_at FROM sessions WHERE id = ?`, id))
	if err != nil {
		return false, fmt.Errorf("read revoked session: %w", err)
	}
	event, err := factory(item)
	if err != nil {
		return false, fmt.Errorf("build logout event: %w", err)
	}
	if err := r.events.AppendTx(ctx, tx, event); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit session revoke: %w", err)
	}
	return true, nil
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
