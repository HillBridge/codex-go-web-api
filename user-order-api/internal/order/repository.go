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

var (
	ErrNotFound            = errors.New("order not found")
	ErrIdempotencyConflict = errors.New("idempotency key conflicts with existing order")
	ErrInvalidState        = errors.New("invalid order state transition")
)

type Repository interface {
	Create(ctx context.Context, input CreateOrderRequest) (Order, bool, error)
	List(ctx context.Context, request page.Request) (page.Result[Order], error)
	FindByID(ctx context.Context, id int64) (Order, error)
	Transition(ctx context.Context, id int64, target Status) (Order, bool, error)
}

type MemoryRepository struct {
	mu     sync.RWMutex
	nextID int64
	orders map[int64]Order
	keys   map[string]int64
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		nextID: 1,
		orders: make(map[int64]Order),
		keys:   make(map[string]int64),
	}
}

func (r *MemoryRepository) Create(ctx context.Context, input CreateOrderRequest) (Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if input.IdempotencyKey != "" {
		if id, exists := r.keys[input.IdempotencyKey]; exists {
			existing := r.orders[id]
			if existing.UserID == input.UserID && existing.Amount == input.Amount {
				return existing, true, nil
			}
			return Order{}, false, ErrIdempotencyConflict
		}
	}

	order := Order{
		ID:        r.nextID,
		UserID:    input.UserID,
		Amount:    input.Amount,
		Status:    StatusPending,
		CreatedAt: time.Now().UTC(),
	}
	r.nextID++
	r.orders[order.ID] = order
	if input.IdempotencyKey != "" {
		r.keys[input.IdempotencyKey] = order.ID
	}

	return order, false, nil
}

func (r *MemoryRepository) Transition(ctx context.Context, id int64, target Status) (Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	item, exists := r.orders[id]
	if !exists {
		return Order{}, false, ErrNotFound
	}
	if item.Status == target {
		return item, false, nil
	}
	if item.Status != StatusPending {
		return Order{}, false, ErrInvalidState
	}

	item.Status = target
	r.orders[id] = item
	return item, true, nil
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
