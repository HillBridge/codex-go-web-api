package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"bridge-go/user-order-api/internal/platform/outbox"
	"bridge-go/user-order-api/internal/platform/page"
)

type MySQLRepository struct {
	db     *sql.DB
	events outbox.Repository
}

func NewMySQLRepository(db *sql.DB, events ...outbox.Repository) Repository {
	var eventRepo outbox.Repository
	if len(events) > 0 {
		eventRepo = events[0]
	}
	return &MySQLRepository{db: db, events: eventRepo}
}

func (r *MySQLRepository) CreateWithEvent(ctx context.Context, input CreateUserRequest, factory func(User) (outbox.Event, error)) (User, bool, error) {
	if r.events == nil {
		item, err := r.Create(ctx, input)
		return item, false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, false, fmt.Errorf("begin user creation: %w", err)
	}
	defer tx.Rollback()
	name := strings.TrimSpace(input.Name)
	email := strings.ToLower(strings.TrimSpace(input.Email))
	createdAt := time.Now().UTC()
	result, err := tx.ExecContext(ctx, "INSERT INTO users (name, email, created_at) VALUES (?, ?, ?)", name, email, createdAt)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return User{}, false, ErrEmailTaken
		}
		return User{}, false, fmt.Errorf("insert user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, false, fmt.Errorf("read inserted user ID: %w", err)
	}
	item := User{ID: id, Name: name, Email: email, CreatedAt: createdAt}
	event, err := factory(item)
	if err != nil {
		return User{}, false, fmt.Errorf("build user event: %w", err)
	}
	if err := r.events.AppendTx(ctx, tx, event); err != nil {
		return User{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, false, fmt.Errorf("commit user creation: %w", err)
	}
	return item, true, nil
}

func (r *MySQLRepository) Create(ctx context.Context, input CreateUserRequest) (User, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	result, err := r.db.ExecContext(ctx,
		"INSERT INTO users (name, email, created_at) VALUES (?, ?, UTC_TIMESTAMP(6))",
		strings.TrimSpace(input.Name), email,
	)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("read inserted user ID: %w", err)
	}
	return r.FindByID(ctx, id)
}

func (r *MySQLRepository) List(ctx context.Context, request page.Request) (page.Result[User], error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, email, created_at FROM users WHERE id > ? ORDER BY id ASC LIMIT ?",
		request.AfterID, request.Limit+1,
	)
	if err != nil {
		return page.Result[User]{}, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		item, err := scanUser(rows)
		if err != nil {
			return page.Result[User]{}, err
		}
		users = append(users, item)
	}
	if err := rows.Err(); err != nil {
		return page.Result[User]{}, fmt.Errorf("iterate users: %w", err)
	}
	return paginate(users, request.Limit), nil
}

func (r *MySQLRepository) FindByID(ctx context.Context, id int64) (User, error) {
	item, err := scanUser(r.db.QueryRowContext(ctx, "SELECT id, name, email, created_at FROM users WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("find user: %w", err)
	}
	return item, nil
}

type userScanner interface {
	Scan(...any) error
}

func scanUser(scanner userScanner) (User, error) {
	var item User
	if err := scanner.Scan(&item.ID, &item.Name, &item.Email, &item.CreatedAt); err != nil {
		return User{}, err
	}
	return item, nil
}
