package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"bridge-go/user-order-api/internal/platform/messaging"
)

type Publisher struct {
	repo        Repository
	broker      messaging.Broker
	logger      *slog.Logger
	workerID    string
	interval    time.Duration
	batchSize   int
	maxAttempts int
	lockFor     time.Duration
	now         func() time.Time
}

func NewPublisher(repo Repository, broker messaging.Broker, logger *slog.Logger, workerID string, interval time.Duration, batchSize, maxAttempts int) *Publisher {
	if interval <= 0 {
		interval = time.Second
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	if workerID == "" {
		workerID = "api-publisher"
	}
	return &Publisher{repo: repo, broker: broker, logger: logger, workerID: workerID, interval: interval, batchSize: batchSize, maxAttempts: maxAttempts, lockFor: 30 * time.Second, now: time.Now}
}

func (p *Publisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		if err := p.PublishOnce(ctx); err != nil && p.logger != nil {
			p.logger.ErrorContext(ctx, "outbox publisher cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Publisher) PublishOnce(ctx context.Context) error {
	claimed, err := p.repo.ClaimBatch(ctx, p.workerID, p.now(), p.batchSize, p.lockFor)
	if err != nil {
		return err
	}
	for _, item := range claimed {
		body, err := json.Marshal(item.Event)
		if err == nil {
			err = p.broker.Publish(ctx, item.Event.EventType, body)
		}
		if err == nil {
			if markErr := p.repo.MarkPublished(ctx, item.LockToken, []int64{item.ID}); markErr != nil {
				return markErr
			}
			continue
		}
		dead := item.Attempts >= p.maxAttempts
		backoff := time.Second * time.Duration(math.Min(float64(300), math.Pow(5, float64(item.Attempts-1))))
		if retryErr := p.repo.MarkRetry(ctx, item.LockToken, fmt.Errorf("publish %s: %w", item.Event.EventID, err), p.now().Add(backoff), dead); retryErr != nil {
			return retryErr
		}
	}
	return nil
}
