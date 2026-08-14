package audit

import (
	"context"
	"log/slog"
	"time"
)

type Logger interface {
	Record(ctx context.Context, action string, fields map[string]any)
}

type AsyncLogger struct {
	logger *slog.Logger
}

func NewAsyncLogger(logger *slog.Logger) *AsyncLogger {
	return &AsyncLogger{logger: logger}
}

func (l *AsyncLogger) Record(ctx context.Context, action string, fields map[string]any) {
	go func() {
		auditCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		select {
		case <-ctx.Done():
			l.logger.InfoContext(auditCtx, "audit skipped because request ended", "action", action)
			return
		case <-auditCtx.Done():
			return
		default:
			args := []any{"action", action}
			for key, value := range fields {
				args = append(args, key, value)
			}
			l.logger.InfoContext(auditCtx, "audit record", args...)
		}
	}()
}
