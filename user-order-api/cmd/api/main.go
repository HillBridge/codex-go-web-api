package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bridge-go/user-order-api/internal/order"
	"bridge-go/user-order-api/internal/platform/database"
	"bridge-go/user-order-api/internal/user"
)

func main() {
	config, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.Open(startupCtx, config.MySQLDSN)
	cancelStartup()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("MySQL shutdown failed: %v", err)
		}
	}()

	startupCtx, cancelStartup = context.WithTimeout(context.Background(), 10*time.Second)
	err = database.ApplyMigrations(startupCtx, db)
	cancelStartup()
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", config.Addr)
	if err != nil {
		log.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	application := newApplication(logger, user.NewMySQLRepository(db), order.NewMySQLRepository(db))
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer cancel()
		if err := application.Close(shutdownCtx); err != nil {
			log.Printf("audit shutdown failed: %v", err)
		}
	}()

	server := newHTTPServer(config, application)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("user-order-api listening on http://%s", listener.Addr())
	if err := serveUntilCancelled(ctx, server, listener, config.ShutdownTimeout); err != nil {
		log.Fatal(err)
	}
}
