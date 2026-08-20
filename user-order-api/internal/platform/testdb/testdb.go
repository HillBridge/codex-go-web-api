package testdb

import (
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

const databaseName = "user_order_api_test"

func RequireDSN(t testing.TB, dsn string) string {
	t.Helper()
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set")
	}
	if err := ValidateDSN(dsn); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func ValidateDSN(dsn string) error {
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse MYSQL_TEST_DSN: %w", err)
	}
	if config.DBName != databaseName {
		return fmt.Errorf("MYSQL_TEST_DSN must target %q, got %q", databaseName, config.DBName)
	}
	return nil
}
