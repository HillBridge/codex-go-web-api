package outbox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func NewEvent(eventType, aggregateType string, aggregateID int64, payload any, now time.Time) (Event, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event payload: %w", err)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	event := Event{EventID: NewEventID(), EventType: eventType, AggregateType: aggregateType, AggregateID: aggregateID, Payload: encoded, OccurredAt: now.UTC()}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func NewEventID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("event-%d", time.Now().UnixNano())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

type Status string

const (
	StatusPending    Status = "pending"
	StatusPublishing Status = "publishing"
	StatusPublished  Status = "published"
	StatusDead       Status = "dead"
)

type Event struct {
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   int64           `json:"aggregate_id"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurred_at"`
}

func (e Event) Validate() error {
	if strings.TrimSpace(e.EventID) == "" {
		return fmt.Errorf("event ID is required")
	}
	if strings.TrimSpace(e.EventType) == "" {
		return fmt.Errorf("event type is required")
	}
	if strings.TrimSpace(e.AggregateType) == "" {
		return fmt.Errorf("aggregate type is required")
	}
	if e.AggregateID <= 0 {
		return fmt.Errorf("aggregate ID must be positive")
	}
	if len(bytes.TrimSpace(e.Payload)) == 0 || bytes.Equal(bytes.TrimSpace(e.Payload), []byte("null")) {
		return fmt.Errorf("event payload is required")
	}
	var value any
	if err := json.Unmarshal(e.Payload, &value); err != nil {
		return fmt.Errorf("event payload must be valid JSON: %w", err)
	}
	if containsSensitiveKey(value) {
		return fmt.Errorf("event payload contains sensitive field")
	}
	return nil
}

func containsSensitiveKey(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, nested := range item {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			switch normalized {
			case "password", "passwordhash", "accesstoken", "refreshtoken", "cookie", "setcookie", "authorization":
				return true
			}
			if containsSensitiveKey(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range item {
			if containsSensitiveKey(nested) {
				return true
			}
		}
	}
	return false
}
