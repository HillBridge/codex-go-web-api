package outbox

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEventValidateAcceptsOrderCreated(t *testing.T) {
	event := Event{
		EventID:       "event-1",
		EventType:     "order.created",
		AggregateType: "order",
		AggregateID:   42,
		Payload:       json.RawMessage(`{"orderID":42,"userID":7}`),
		OccurredAt:    time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("Event.Validate() error = %v", err)
	}
}

func TestEventValidateRejectsMissingRequiredFields(t *testing.T) {
	cases := []Event{
		{EventType: "order.created", AggregateType: "order", AggregateID: 1, Payload: json.RawMessage(`{}`)},
		{EventID: "event-1", AggregateType: "order", AggregateID: 1, Payload: json.RawMessage(`{}`)},
		{EventID: "event-1", EventType: "order.created", AggregateType: "order", AggregateID: 1},
	}
	for _, event := range cases {
		if err := event.Validate(); err == nil {
			t.Fatalf("Event.Validate() error = nil for %#v", event)
		}
	}
}

func TestEventValidateRejectsSensitivePayloadKeys(t *testing.T) {
	for _, key := range []string{"password", "accessToken", "refreshToken", "cookie"} {
		event := Event{
			EventID:       "event-1",
			EventType:     "auth.logged_in",
			AggregateType: "user",
			AggregateID:   7,
			Payload:       json.RawMessage(`{"` + key + `":"secret"}`),
		}
		if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("Event.Validate() error = %v for key %q, want sensitive-field error", err, key)
		}
	}
}
