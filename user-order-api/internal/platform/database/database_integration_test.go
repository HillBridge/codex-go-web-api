package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestApplyMigrationsIsIdempotentAndCreatesForeignKey(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := db.Stats().MaxOpenConnections; got != 10 {
		t.Fatalf("MaxOpenConnections = %d, want 10", got)
	}

	resetSchema(t, ctx, db)
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("first ApplyMigrations() error = %v", err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("second ApplyMigrations() error = %v", err)
	}

	var migrations int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if migrations != 2 {
		t.Fatalf("migration count = %d, want 2", migrations)
	}
	for _, table := range []string{"users", "orders"} {
		var count int
		if err := db.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM information_schema.tables
			WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %q count = %d, want 1", table, count)
		}
	}

	_, err = db.ExecContext(ctx, "INSERT INTO orders (user_id, amount, status, created_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6))", 999, 100, "pending")
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1452 {
		t.Fatalf("foreign-key error = %v, want MySQL error 1452", err)
	}
}

func resetSchema(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		"DROP TABLE IF EXISTS orders",
		"DROP TABLE IF EXISTS users",
		"DROP TABLE IF EXISTS schema_migrations",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
}
