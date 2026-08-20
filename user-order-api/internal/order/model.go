package order

import "time"

type Status string

const (
	StatusPending   Status = "pending"
	StatusPaid      Status = "paid"
	StatusCancelled Status = "cancelled"
)

type Order struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"userId"`
	Amount    int64     `json:"amount"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type CreateOrderRequest struct {
	UserID                 int64  `json:"userId"`
	Amount                 int64  `json:"amount"`
	IdempotencyKey         string `json:"-"`
	IdempotencyKeyProvided bool   `json:"-"`
}
