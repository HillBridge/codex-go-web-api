package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/rabbitmq/amqp091-go"
)

const (
	DefaultExchange = "user-order-api.events"
	DefaultQueue    = "user-order-api.audit.v1"
	RetryQueue      = "user-order-api.audit.retry"
	DeadQueue       = "user-order-api.audit.dlq"
)

type Broker interface {
	Publish(context.Context, string, []byte) error
	Consume(context.Context) (<-chan Delivery, error)
	Close() error
}

type Delivery struct {
	EventID string
	Body    []byte
	Attempt int
	ack     func() error
	retry   func(int) error
	dead    func() error
}

func (d Delivery) Ack() error        { return d.ack() }
func (d Delivery) Retry() error      { return d.retry(d.Attempt + 1) }
func (d Delivery) DeadLetter() error { return d.dead() }

type RabbitBroker struct {
	conn       *amqp091.Connection
	publish    *amqp091.Channel
	consume    *amqp091.Channel
	exchange   string
	queue      string
	retryQueue string
	deadQueue  string
	prefetch   int
	confirms   chan amqp091.Confirmation
	mu         sync.Mutex
}

func NewBroker(ctx context.Context, url, exchange, queue string, prefetch int) (*RabbitBroker, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("rabbitmq URL is required")
	}
	if exchange == "" {
		exchange = DefaultExchange
	}
	if queue == "" {
		queue = DefaultQueue
	}
	if prefetch <= 0 {
		prefetch = 20
	}
	conn, err := dialWithContext(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect rabbitmq: %w", err)
	}
	publish, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open publisher channel: %w", err)
	}
	consume, err := conn.Channel()
	if err != nil {
		_ = publish.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("open consumer channel: %w", err)
	}
	b := &RabbitBroker{conn: conn, publish: publish, consume: consume, exchange: exchange, queue: queue, retryQueue: queue + ".retry", deadQueue: queue + ".dlq", prefetch: prefetch}
	if err := b.declareTopology(); err != nil {
		_ = b.Close()
		return nil, err
	}
	return b, nil
}

func dialWithContext(ctx context.Context, url string) (*amqp091.Connection, error) {
	type result struct {
		conn *amqp091.Connection
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		conn, err := amqp091.Dial(url)
		resultCh <- result{conn: conn, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.conn, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *RabbitBroker) declareTopology() error {
	if err := b.publish.Confirm(false); err != nil {
		return fmt.Errorf("enable publisher confirms: %w", err)
	}
	b.confirms = b.publish.NotifyPublish(make(chan amqp091.Confirmation, 1))
	if err := b.publish.ExchangeDeclare(b.exchange, amqp091.ExchangeTopic, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange: %w", err)
	}
	if _, err := b.publish.QueueDeclare(b.queue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare audit queue: %w", err)
	}
	if err := b.publish.QueueBind(b.queue, "#", b.exchange, false, nil); err != nil {
		return fmt.Errorf("bind audit queue: %w", err)
	}
	retryArgs := amqp091.Table{"x-message-ttl": int32(5000), "x-dead-letter-exchange": b.exchange, "x-dead-letter-routing-key": "#"}
	if _, err := b.publish.QueueDeclare(b.retryQueue, true, false, false, false, retryArgs); err != nil {
		return fmt.Errorf("declare retry queue: %w", err)
	}
	if _, err := b.publish.QueueDeclare(b.deadQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead-letter queue: %w", err)
	}
	if err := b.consume.Qos(b.prefetch, 0, false); err != nil {
		return fmt.Errorf("set consumer prefetch: %w", err)
	}
	return nil
}

func (b *RabbitBroker) Publish(ctx context.Context, routingKey string, body []byte) error {
	var event struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("decode event: %w", err)
	}
	if event.EventID == "" || event.EventType == "" {
		return fmt.Errorf("event ID and type are required")
	}
	if routingKey == "" {
		routingKey = event.EventType
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.publish.PublishWithContext(ctx, b.exchange, routingKey, false, false, amqp091.Publishing{ContentType: "application/json", DeliveryMode: amqp091.Persistent, MessageId: event.EventID, Body: body, Headers: amqp091.Table{"event-id": event.EventID}}); err != nil {
		return fmt.Errorf("publish event: %w", err)
	}
	select {
	case confirmation := <-b.confirms:
		if !confirmation.Ack {
			return fmt.Errorf("rabbitmq publish was nacked")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *RabbitBroker) Consume(ctx context.Context) (<-chan Delivery, error) {
	deliveries, err := b.consume.Consume(b.queue, "", false, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("consume audit queue: %w", err)
	}
	out := make(chan Delivery)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case item, ok := <-deliveries:
				if !ok {
					return
				}
				attempt := headerInt(item.Headers, "attempt")
				if attempt <= 0 {
					attempt = 1
				}
				eventID := item.MessageId
				if eventID == "" {
					eventID = headerString(item.Headers, "event-id")
				}
				wrapped := Delivery{EventID: eventID, Body: item.Body, Attempt: attempt,
					ack: func() error { return item.Ack(false) },
					retry: func(next int) error {
						return b.publishRetry(item, next)
					},
					dead: func() error {
						return b.publishDead(item)
					},
				}
				select {
				case out <- wrapped:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (b *RabbitBroker) publishRetry(item amqp091.Delivery, attempt int) error {
	headers := amqp091.Table{"attempt": attempt, "event-id": item.MessageId}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.publish.Publish("", b.retryQueue, false, false, amqp091.Publishing{ContentType: item.ContentType, DeliveryMode: amqp091.Persistent, MessageId: item.MessageId, Headers: headers, Body: item.Body}); err != nil {
		return err
	}
	return item.Ack(false)
}

func (b *RabbitBroker) publishDead(item amqp091.Delivery) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.publish.Publish("", b.deadQueue, false, false, amqp091.Publishing{ContentType: item.ContentType, DeliveryMode: amqp091.Persistent, MessageId: item.MessageId, Body: item.Body}); err != nil {
		return err
	}
	return item.Ack(false)
}

func (b *RabbitBroker) Close() error {
	if b.consume != nil {
		_ = b.consume.Close()
	}
	if b.publish != nil {
		_ = b.publish.Close()
	}
	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}

func (b *RabbitBroker) Ready() bool {
	return b != nil && b.conn != nil && !b.conn.IsClosed()
}

func headerString(headers amqp091.Table, key string) string {
	value, _ := headers[key].(string)
	return value
}

func headerInt(headers amqp091.Table, key string) int {
	switch value := headers[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		return int(value)
	}
	return 0
}

var _ Broker = (*RabbitBroker)(nil)
