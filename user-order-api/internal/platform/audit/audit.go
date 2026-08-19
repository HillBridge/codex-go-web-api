package audit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

const defaultQueueSize = 256

type Logger interface {
	Record(ctx context.Context, action string, fields map[string]any)
}

type AsyncLogger struct {
	logger     *slog.Logger
	events     chan event
	done       chan struct{}
	mu         sync.RWMutex
	closed     bool
	dropped    atomic.Uint64
	dropWarned atomic.Bool
	dropReason atomic.Value
}

func NewAsyncLogger(logger *slog.Logger) *AsyncLogger {
	return newAsyncLogger(logger, defaultQueueSize)
}

func newAsyncLogger(logger *slog.Logger, queueSize int) *AsyncLogger {
	l := &AsyncLogger{
		logger: logger,
		events: make(chan event, queueSize),
		done:   make(chan struct{}),
	}
	l.dropReason.Store("")

	go l.run()
	return l
}

func (l *AsyncLogger) Record(ctx context.Context, action string, fields map[string]any) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.closed {
		l.drop("logger is closed")
		return
	}

	item := event{ctx: ctx, action: action, fields: cloneFields(fields)}
	select {
	case l.events <- item:
	default:
		l.drop("queue is full")
	}
}

func (l *AsyncLogger) Close(ctx context.Context) error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.events)
	}
	l.mu.Unlock()

	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *AsyncLogger) Dropped() uint64 {
	return l.dropped.Load()
}

func (l *AsyncLogger) run() {
	defer close(l.done)
	for item := range l.events {
		args := []any{"action", item.action}
		for key, value := range item.fields {
			args = append(args, key, value)
		}
		l.logger.InfoContext(item.ctx, "audit record", args...)
		l.reportDrops()
	}
	l.reportDrops()
}

func (l *AsyncLogger) drop(reason string) {
	l.dropped.Add(1)
	l.dropReason.Store(reason)
}

func (l *AsyncLogger) reportDrops() {
	if l.dropped.Load() == 0 || !l.dropWarned.CompareAndSwap(false, true) {
		return
	}
	l.logger.Warn("audit event dropped", "reason", l.dropReason.Load().(string), "dropped", l.dropped.Load())
}

type event struct {
	ctx    context.Context
	action string
	fields map[string]any
}

func cloneFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}

	copy := make(map[string]any, len(fields))
	for key, value := range fields {
		copy[key] = value
	}
	return copy
}
