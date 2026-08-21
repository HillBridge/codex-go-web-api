package order

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"

	"bridge-go/user-order-api/internal/platform/page"
)

var ErrUserNotFound = errors.New("order user not found")

type MySQLRepository struct {
	db *sql.DB
}

func NewMySQLRepository(db *sql.DB) Repository {
	return &MySQLRepository{db: db}
}

func (r *MySQLRepository) Create(ctx context.Context, input CreateOrderRequest) (Order, bool, error) {
	if input.IdempotencyKey == "" {
		item, err := r.insert(ctx, input)
		return item, false, err
	}

	result, err := r.db.ExecContext(ctx,
		"INSERT INTO orders (user_id, amount, status, idempotency_key, created_at) VALUES (?, ?, ?, ?, UTC_TIMESTAMP(6))",
		input.UserID, input.Amount, StatusPending, input.IdempotencyKey,
	)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1452 {
			return Order{}, false, ErrUserNotFound
		}
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			existing, findErr := r.findByIdempotencyKey(ctx, input.IdempotencyKey)
			if findErr != nil {
				return Order{}, false, findErr
			}
			if existing.UserID == input.UserID && existing.Amount == input.Amount {
				return existing, true, nil
			}
			return Order{}, false, ErrIdempotencyConflict
		}
		return Order{}, false, fmt.Errorf("insert order: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Order{}, false, fmt.Errorf("read inserted order ID: %w", err)
	}
	item, err := r.FindByID(ctx, id)
	return item, false, err
}

func (r *MySQLRepository) insert(ctx context.Context, input CreateOrderRequest) (Order, error) {
	result, err := r.db.ExecContext(ctx,
		"INSERT INTO orders (user_id, amount, status, created_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6))",
		input.UserID, input.Amount, StatusPending,
	)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1452 {
			return Order{}, ErrUserNotFound
		}
		return Order{}, fmt.Errorf("insert order: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Order{}, fmt.Errorf("read inserted order ID: %w", err)
	}
	return r.FindByID(ctx, id)
}

func (r *MySQLRepository) List(ctx context.Context, request page.Request) (page.Result[Order], error) {
	return r.list(ctx, request, 0)
}

func (r *MySQLRepository) ListByUserID(ctx context.Context, userID int64, request page.Request) (page.Result[Order], error) {
	return r.list(ctx, request, userID)
}

func (r *MySQLRepository) list(ctx context.Context, request page.Request, userID int64) (page.Result[Order], error) {
	query := "SELECT id, user_id, amount, status, created_at FROM orders WHERE id > ?"
	args := []any{request.AfterID}
	if userID > 0 {
		query += " AND user_id = ?"
		args = append(args, userID)
	}
	query += " ORDER BY id ASC LIMIT ?"
	args = append(args, request.Limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page.Result[Order]{}, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	orders := make([]Order, 0)
	for rows.Next() {
		item, err := scanOrder(rows)
		if err != nil {
			return page.Result[Order]{}, err
		}
		orders = append(orders, item)
	}
	if err := rows.Err(); err != nil {
		return page.Result[Order]{}, fmt.Errorf("iterate orders: %w", err)
	}
	return paginate(orders, request.Limit), nil
}

func (r *MySQLRepository) FindByID(ctx context.Context, id int64) (Order, error) {
	item, err := scanOrder(r.db.QueryRowContext(ctx, "SELECT id, user_id, amount, status, created_at FROM orders WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("find order: %w", err)
	}
	return item, nil
}

func (r *MySQLRepository) Transition(ctx context.Context, id int64, target Status) (Order, bool, error) {
	result, err := r.db.ExecContext(ctx,
		"UPDATE orders SET status = ? WHERE id = ? AND status = ?",
		target, id, StatusPending,
	)
	if err != nil {
		return Order{}, false, fmt.Errorf("transition order: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Order{}, false, fmt.Errorf("read transition result: %w", err)
	}
	item, err := r.FindByID(ctx, id)
	if err != nil {
		return Order{}, false, err
	}
	if changed > 0 {
		return item, true, nil
	}
	if item.Status == target {
		return item, false, nil
	}
	return Order{}, false, ErrInvalidState
}

func (r *MySQLRepository) findByIdempotencyKey(ctx context.Context, key string) (Order, error) {
	item, err := scanOrder(r.db.QueryRowContext(ctx,
		"SELECT id, user_id, amount, status, created_at FROM orders WHERE idempotency_key = ?", key,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("find order by idempotency key: %w", err)
	}
	return item, nil
}

type orderScanner interface {
	Scan(...any) error
}

func scanOrder(scanner orderScanner) (Order, error) {
	var item Order
	var status string
	if err := scanner.Scan(&item.ID, &item.UserID, &item.Amount, &status, &item.CreatedAt); err != nil {
		return Order{}, err
	}
	item.Status = Status(status)
	return item, nil
}
