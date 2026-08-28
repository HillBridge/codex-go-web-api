package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"bridge-go/user-order-api/internal/platform/messaging"
	"bridge-go/user-order-api/internal/platform/outbox"
)

type EventHandler func(context.Context, outbox.Event) error

type Consumer struct {
	inbox        outbox.Inbox
	broker       messaging.Broker
	consumerName string
	maxRetries   int
	lockFor      time.Duration
	logger       *slog.Logger
	handler      EventHandler
	now          func() time.Time
}

func NewConsumer(inbox outbox.Inbox, broker messaging.Broker, consumerName string, maxRetries int, logger *slog.Logger, handler EventHandler) *Consumer {
	if consumerName == "" {
		consumerName = "audit-v1"
	}
	if maxRetries <= 0 {
		maxRetries = 5
	}
	if handler == nil {
		handler = LogEvent
	}
	return &Consumer{inbox: inbox, broker: broker, consumerName: consumerName, maxRetries: maxRetries, lockFor: 30 * time.Second, logger: logger, handler: handler, now: time.Now}
}

func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.broker.Consume(ctx)
	if err != nil {
		return err
	}
	for delivery := range deliveries {
		if err := c.process(ctx, delivery); err != nil && c.logger != nil {
			c.logger.ErrorContext(ctx, "audit event processing failed", "event_id", delivery.EventID, "attempt", delivery.Attempt, "error", err)
		}
	}
	return ctx.Err()
}

func (c *Consumer) process(ctx context.Context, delivery messaging.Delivery) error {
	var event outbox.Event
	if err := json.Unmarshal(delivery.Body, &event); err != nil {
		_ = delivery.DeadLetter()
		return fmt.Errorf("decode audit event: %w", err)
	}
	if err := event.Validate(); err != nil {
		_ = delivery.DeadLetter()
		return fmt.Errorf("validate audit event: %w", err)
	}
	eventID := delivery.EventID
	if eventID == "" {
		eventID = event.EventID
	}
	claimed, err := c.inbox.ClaimInbox(ctx, c.consumerName, eventID, c.now(), c.lockFor)
	if err != nil {
		return err
	}
	if !claimed {
		return delivery.Ack()
	}
	if err := c.handler(ctx, event); err != nil {
		retryAt := c.now().Add(time.Duration(delivery.Attempt) * time.Second)
		_ = c.inbox.MarkInboxFailed(ctx, c.consumerName, eventID, err, retryAt)
		if delivery.Attempt >= c.maxRetries {
			_ = delivery.DeadLetter()
		} else {
			_ = delivery.Retry()
		}
		return err
	}
	if err := c.inbox.MarkInboxProcessed(ctx, c.consumerName, eventID, c.now()); err != nil {
		return err
	}
	return delivery.Ack()
}

func LogEvent(ctx context.Context, event outbox.Event) error {
	logger := slog.Default()
	logger.InfoContext(ctx, "audit event", "event_id", event.EventID, "event_type", event.EventType, "aggregate_type", event.AggregateType, "aggregate_id", event.AggregateID, "occurred_at", event.OccurredAt)
	return nil
}
