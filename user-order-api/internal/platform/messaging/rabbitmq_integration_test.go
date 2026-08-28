package messaging_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/platform/messaging"
	"bridge-go/user-order-api/internal/platform/outbox"
)

func TestRabbitBrokerPublishAndConsume(t *testing.T) {
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	broker, err := messaging.NewBroker(ctx, url, "user-order-api.test.events", "user-order-api.test.audit.v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	event, err := outbox.NewEvent("test.created", "test", 1, map[string]any{"test": true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(ctx, event.EventType, body); err != nil {
		t.Fatal(err)
	}
	deliveries, err := broker.Consume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case delivery := <-deliveries:
		if delivery.EventID != event.EventID {
			t.Fatalf("event ID = %q, want %q", delivery.EventID, event.EventID)
		}
		if err := delivery.Ack(); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
