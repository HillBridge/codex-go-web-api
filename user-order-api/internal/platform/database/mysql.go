package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/go-sql-driver/mysql"
	"go.opentelemetry.io/otel/trace"
)

const (
	maxOpenConnections = 10
	maxIdleConnections = 5
	connectionLifetime = 30 * time.Minute
	pingTimeout        = 5 * time.Second
)

// Open creates and verifies a MySQL connection pool.
func Open(ctx context.Context, dsn string, providers ...trace.TracerProvider) (*sql.DB, error) {
	provider := trace.NewNoopTracerProvider()
	if len(providers) > 0 && providers[0] != nil {
		provider = providers[0]
	}
	db, err := otelsql.Open("mysql", dsn,
		otelsql.WithTracerProvider(provider),
		otelsql.WithSpanOptions(otelsql.SpanOptions{DisableQuery: true}),
	)
	if err != nil {
		return nil, fmt.Errorf("open MySQL: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxLifetime(connectionLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping MySQL: %w", err)
	}

	return db, nil
}
