package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAsyncLoggerRecordsCancelledRequestContext(t *testing.T) {
	var output bytes.Buffer
	logger := NewAsyncLogger(slog.New(slog.NewTextHandler(&output, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger.Record(ctx, "user.created", map[string]any{"userID": int64(1)})

	closeAuditLogger(t, logger)

	if got := output.String(); !strings.Contains(got, "msg=\"audit record\"") {
		t.Fatalf("audit output = %q, want an audit record", got)
	}
}

func TestAsyncLoggerDropsWhenQueueIsFullWithoutBlocking(t *testing.T) {
	handler := newBlockingHandler()
	logger := newAsyncLogger(slog.New(handler), 1)

	logger.Record(context.Background(), "first", nil)
	select {
	case <-handler.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin writing the first audit event")
	}

	logger.Record(context.Background(), "second", nil)
	returned := make(chan struct{})
	go func() {
		logger.Record(context.Background(), "third", nil)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Record blocked when the queue was full")
	}
	if got := logger.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want %d", got, 1)
	}

	close(handler.release)
	closeAuditLogger(t, logger)
}

func TestAsyncLoggerReportsPendingEvents(t *testing.T) {
	worker := newBlockingHandler()
	logger := newAsyncLogger(slog.New(worker), 1)
	logger.Record(context.Background(), "first", nil)
	<-worker.started
	logger.Record(context.Background(), "second", nil)

	if got := logger.Pending(); got != 1 {
		t.Fatalf("Pending() = %d, want 1", got)
	}

	close(worker.release)
	closeAuditLogger(t, logger)
}

func TestAsyncLoggerCloseDrainsQueuedEvents(t *testing.T) {
	var output bytes.Buffer
	logger := NewAsyncLogger(slog.New(slog.NewTextHandler(&output, nil)))
	logger.Record(context.Background(), "order.created", map[string]any{"orderID": int64(1)})

	closeAuditLogger(t, logger)

	if got := output.String(); !strings.Contains(got, "action=order.created") {
		t.Fatalf("audit output = %q, want drained order.created event", got)
	}
}

func closeAuditLogger(t *testing.T, logger *AsyncLogger) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := logger.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

type blockingHandler struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func newBlockingHandler() *blockingHandler {
	return &blockingHandler{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (h *blockingHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *blockingHandler) Handle(context.Context, slog.Record) error {
	h.startedOnce.Do(func() { close(h.started) })
	<-h.release
	return nil
}

func (h *blockingHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *blockingHandler) WithGroup(string) slog.Handler {
	return h
}
