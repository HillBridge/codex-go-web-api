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

func (r *MySQLRepository) Create(ctx context.Context, input CreateOrderRequest) (Order, error) {
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
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, user_id, amount, status, created_at FROM orders WHERE id > ? ORDER BY id ASC LIMIT ?",
		request.AfterID, request.Limit+1,
	)
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
