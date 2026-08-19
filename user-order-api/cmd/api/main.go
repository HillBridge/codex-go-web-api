package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	config, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", config.Addr)
	if err != nil {
		log.Fatal(err)
	}

	application := newServer()
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
