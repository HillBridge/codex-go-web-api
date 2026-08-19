package order

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"bridge-go/user-order-api/internal/platform/page"
)

var ErrNotFound = errors.New("order not found")

type Repository interface {
	Create(ctx context.Context, input CreateOrderRequest) (Order, error)
	List(ctx context.Context, request page.Request) (page.Result[Order], error)
	FindByID(ctx context.Context, id int64) (Order, error)
}

type MemoryRepository struct {
	mu     sync.RWMutex
	nextID int64
	orders map[int64]Order
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		nextID: 1,
		orders: make(map[int64]Order),
	}
}

func (r *MemoryRepository) Create(ctx context.Context, input CreateOrderRequest) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	order := Order{
		ID:        r.nextID,
		UserID:    input.UserID,
		Amount:    input.Amount,
		Status:    StatusPending,
		CreatedAt: time.Now().UTC(),
	}
	r.nextID++
	r.orders[order.ID] = order

	return order, nil
}

func (r *MemoryRepository) List(ctx context.Context, request page.Request) (page.Result[Order], error) {
	if err := ctx.Err(); err != nil {
		return page.Result[Order]{}, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	orders := make([]Order, 0, len(r.orders))
	for _, item := range r.orders {
		if item.ID > request.AfterID {
			orders = append(orders, item)
		}
	}
	slices.SortFunc(orders, func(a, b Order) int {
		return int(a.ID - b.ID)
	})

	return paginate(orders, request.Limit), nil
}

func paginate(items []Order, limit int) page.Result[Order] {
	if len(items) <= limit {
		return page.Result[Order]{Items: items}
	}
	items = items[:limit]
	return page.Result[Order]{Items: items, NextCursor: strconv.FormatInt(items[len(items)-1].ID, 10)}
}

func (r *MemoryRepository) FindByID(ctx context.Context, id int64) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	order, exists := r.orders[id]
	if !exists {
		return Order{}, ErrNotFound
	}

	return order, nil
}
