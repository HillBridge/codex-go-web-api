package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/platform/testdb"
)

func TestMySQLRepositoryAppendClaimAndMark(t *testing.T) {
	dsn := testdb.RequireDSN(t, os.Getenv("MYSQL_TEST_DSN"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	clearOutboxTestRows(t, ctx, db)

	eventID := fmt.Sprintf("event-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM outbox_events WHERE event_id = ?", eventID)
	})
	repo := NewMySQLRepository(db)
	event := Event{EventID: eventID, EventType: "order.created", AggregateType: "order", AggregateID: 42, Payload: json.RawMessage(`{"orderID":42}`), OccurredAt: time.Now().UTC()}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendTx(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events WHERE event_id = ?", eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back event count = %d, want 0", count)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendTx(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	claimed, err := repo.ClaimBatch(ctx, "worker-1", time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Event.EventID != eventID || claimed[0].LockToken == "" {
		t.Fatalf("claimed = %#v, want one locked event", claimed)
	}
	if err := repo.MarkPublished(ctx, claimed[0].LockToken, []int64{claimed[0].ID}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM outbox_events WHERE event_id = ?", eventID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if Status(status) != StatusPublished {
		t.Fatalf("status = %q, want published", status)
	}
}

func TestMySQLRepositoryInboxClaimIsIdempotent(t *testing.T) {
	dsn := testdb.RequireDSN(t, os.Getenv("MYSQL_TEST_DSN"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	clearOutboxTestRows(t, ctx, db)
	eventID := fmt.Sprintf("event-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM inbox_events WHERE consumer_name = ? AND event_id = ?", "audit-test", eventID)
	})
	repo := NewMySQLRepository(db)
	now := time.Now().UTC()
	claimed, err := repo.ClaimInbox(ctx, "audit-test", eventID, now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("first ClaimInbox = claimed=%v err=%v, want true", claimed, err)
	}
	if err := repo.MarkInboxProcessed(ctx, "audit-test", eventID, now); err != nil {
		t.Fatal(err)
	}
	claimed, err = repo.ClaimInbox(ctx, "audit-test", eventID, now, time.Minute)
	if err != nil || claimed {
		t.Fatalf("duplicate ClaimInbox = claimed=%v err=%v, want false", claimed, err)
	}
}

func clearOutboxTestRows(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"inbox_events", "outbox_events"} {
		if _, err := db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
}
