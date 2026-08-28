package outbox

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ClaimedEvent struct {
	ID        int64
	Event     Event
	LockToken string
	Attempts  int
}

type Repository interface {
	AppendTx(context.Context, *sql.Tx, Event) error
	ClaimBatch(context.Context, string, time.Time, int, time.Duration) ([]ClaimedEvent, error)
	MarkPublished(context.Context, string, []int64) error
	MarkRetry(context.Context, string, error, time.Time, bool) error
}

type Inbox interface {
	ClaimInbox(context.Context, string, string, time.Time, time.Duration) (bool, error)
	MarkInboxProcessed(context.Context, string, string, time.Time) error
	MarkInboxFailed(context.Context, string, string, error, time.Time) error
}

type MySQLRepository struct {
	db *sql.DB
}

func NewMySQLRepository(db *sql.DB) *MySQLRepository {
	return &MySQLRepository{db: db}
}

func (r *MySQLRepository) AppendTx(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate outbox event: %w", err)
	}
	now := event.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO outbox_events
			(event_id, event_type, aggregate_type, aggregate_id, payload, occurred_at, status, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.EventType, event.AggregateType, event.AggregateID, []byte(event.Payload), now, StatusPending, now, now, now,
	)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

func (r *MySQLRepository) ClaimBatch(ctx context.Context, workerID string, now time.Time, limit int, lockFor time.Duration) ([]ClaimedEvent, error) {
	if limit < 1 {
		return nil, fmt.Errorf("outbox batch size must be positive")
	}
	if lockFor <= 0 {
		return nil, fmt.Errorf("outbox lock duration must be positive")
	}
	now = now.UTC()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, event_id, event_type, aggregate_type, aggregate_id, payload, occurred_at, attempts
		FROM outbox_events
		WHERE (status = 'pending' AND available_at <= ?)
		   OR (status = 'publishing' AND (locked_until IS NULL OR locked_until <= ?))
		ORDER BY id ASC
		LIMIT ?
		FOR UPDATE SKIP LOCKED`, now, now, limit)
	if err != nil {
		return nil, fmt.Errorf("select outbox events: %w", err)
	}
	items := make([]ClaimedEvent, 0, limit)
	for rows.Next() {
		var item ClaimedEvent
		var payload []byte
		var occurredAt time.Time
		if err := rows.Scan(&item.ID, &item.Event.EventID, &item.Event.EventType, &item.Event.AggregateType, &item.Event.AggregateID, &payload, &occurredAt, &item.Attempts); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		item.Event.Payload = json.RawMessage(payload)
		item.Event.OccurredAt = occurredAt
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close outbox events: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox events: %w", err)
	}

	claimed := make([]ClaimedEvent, 0, len(items))
	lockToken := newLockToken(workerID)
	lockedUntil := now.Add(lockFor)
	for _, item := range items {
		item.LockToken = lockToken
		if _, err := tx.ExecContext(ctx, `
			UPDATE outbox_events
			SET status = 'publishing', attempts = attempts + 1, locked_until = ?, lock_token = ?, updated_at = ?
			WHERE id = ?`, lockedUntil, lockToken, now, item.ID); err != nil {
			return nil, fmt.Errorf("lock outbox event: %w", err)
		}
		item.Attempts++
		claimed = append(claimed, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return claimed, nil
}

func (r *MySQLRepository) MarkPublished(ctx context.Context, lockToken string, ids []int64) error {
	if strings.TrimSpace(lockToken) == "" || len(ids) == 0 {
		return fmt.Errorf("lock token and event IDs are required")
	}
	for _, id := range ids {
		result, err := r.db.ExecContext(ctx, `
			UPDATE outbox_events
			SET status = 'published', published_at = UTC_TIMESTAMP(6), locked_until = NULL, lock_token = NULL, updated_at = UTC_TIMESTAMP(6)
			WHERE id = ? AND status = 'publishing' AND lock_token = ?`, id, lockToken)
		if err != nil {
			return fmt.Errorf("mark outbox event published: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read published result: %w", err)
		}
		if changed != 1 {
			return fmt.Errorf("outbox event %d is not owned by lock token", id)
		}
	}
	return nil
}

func (r *MySQLRepository) MarkRetry(ctx context.Context, lockToken string, cause error, availableAt time.Time, dead bool) error {
	if strings.TrimSpace(lockToken) == "" {
		return fmt.Errorf("lock token is required")
	}
	status := StatusPending
	if dead {
		status = StatusDead
	}
	message := ""
	if cause != nil {
		message = cause.Error()
		if len(message) > 1000 {
			message = message[:1000]
		}
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = ?, available_at = ?, locked_until = NULL, lock_token = NULL, last_error = ?, updated_at = UTC_TIMESTAMP(6)
		WHERE status = 'publishing' AND lock_token = ?`, status, availableAt.UTC(), message, lockToken)
	if err != nil {
		return fmt.Errorf("mark outbox retry: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbox retry result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("outbox lock token is not active")
	}
	return nil
}

func (r *MySQLRepository) ClaimInbox(ctx context.Context, consumerName, eventID string, now time.Time, lockFor time.Duration) (bool, error) {
	if strings.TrimSpace(consumerName) == "" || strings.TrimSpace(eventID) == "" || lockFor <= 0 {
		return false, fmt.Errorf("consumer, event ID and lock duration are required")
	}
	now = now.UTC()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin inbox claim: %w", err)
	}
	defer tx.Rollback()
	var status string
	var lockedUntil sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT status, locked_until FROM inbox_events WHERE consumer_name = ? AND event_id = ? FOR UPDATE`, consumerName, eventID).Scan(&status, &lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO inbox_events (consumer_name, event_id, status, attempts, locked_until, updated_at) VALUES (?, ?, 'processing', 1, ?, ?)`, consumerName, eventID, now.Add(lockFor), now); err != nil {
			return false, fmt.Errorf("insert inbox event: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit inbox claim: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("find inbox event: %w", err)
	}
	if status == "processed" || (lockedUntil.Valid && lockedUntil.Time.After(now)) {
		_ = tx.Rollback()
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inbox_events SET status = 'processing', attempts = attempts + 1, locked_until = ?, last_error = NULL, updated_at = ? WHERE consumer_name = ? AND event_id = ?`, now.Add(lockFor), now, consumerName, eventID); err != nil {
		return false, fmt.Errorf("reclaim inbox event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit inbox reclaim: %w", err)
	}
	return true, nil
}

func (r *MySQLRepository) MarkInboxProcessed(ctx context.Context, consumerName, eventID string, processedAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `UPDATE inbox_events SET status = 'processed', processed_at = ?, locked_until = NULL, updated_at = ? WHERE consumer_name = ? AND event_id = ?`, processedAt.UTC(), processedAt.UTC(), consumerName, eventID)
	if err != nil {
		return fmt.Errorf("mark inbox processed: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read inbox processed result: %w", err)
	} else if changed != 1 {
		return fmt.Errorf("inbox event not found")
	}
	return nil
}

func (r *MySQLRepository) MarkInboxFailed(ctx context.Context, consumerName, eventID string, cause error, retryAt time.Time) error {
	message := ""
	if cause != nil {
		message = cause.Error()
		if len(message) > 1000 {
			message = message[:1000]
		}
	}
	_, err := r.db.ExecContext(ctx, `UPDATE inbox_events SET status = 'processing', locked_until = ?, last_error = ?, updated_at = ? WHERE consumer_name = ? AND event_id = ?`, retryAt.UTC(), message, retryAt.UTC(), consumerName, eventID)
	if err != nil {
		return fmt.Errorf("mark inbox failed: %w", err)
	}
	return nil
}

func newLockToken(workerID string) string {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("%s-%d", workerID, time.Now().UnixNano())
	}
	return workerID + "-" + hex.EncodeToString(value)
}
