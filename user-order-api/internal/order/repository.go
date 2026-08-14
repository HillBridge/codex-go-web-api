package order

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

var ErrNotFound = errors.New("order not found")

type Repository interface {
	Create(ctx context.Context, input CreateOrderRequest) (Order, error)
	List(ctx context.Context) ([]Order, error)
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

func (r *MemoryRepository) List(ctx context.Context) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	orders := make([]Order, 0, len(r.orders))
	for _, item := range r.orders {
		orders = append(orders, item)
	}
	slices.SortFunc(orders, func(a, b Order) int {
		return int(a.ID - b.ID)
	})

	return orders, nil
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
