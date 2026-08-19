package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type serverConfig struct {
	MySQLDSN          string
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func defaultServerConfig() serverConfig {
	return serverConfig{
		Addr:              ":8888",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   10 * time.Second,
	}
}

func loadConfig(getenv func(string) string) (serverConfig, error) {
	config := defaultServerConfig()

	if rawPort := strings.TrimSpace(getenv("PORT")); rawPort != "" {
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return serverConfig{}, fmt.Errorf("PORT must be a number between 1 and 65535")
		}
		config.Addr = ":" + strconv.Itoa(port)
	}

	for _, item := range []struct {
		name   string
		target *time.Duration
	}{
		{name: "READ_HEADER_TIMEOUT", target: &config.ReadHeaderTimeout},
		{name: "READ_TIMEOUT", target: &config.ReadTimeout},
		{name: "WRITE_TIMEOUT", target: &config.WriteTimeout},
		{name: "IDLE_TIMEOUT", target: &config.IdleTimeout},
		{name: "SHUTDOWN_TIMEOUT", target: &config.ShutdownTimeout},
	} {
		raw := strings.TrimSpace(getenv(item.name))
		if raw == "" {
			continue
		}

		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			return serverConfig{}, fmt.Errorf("%s must be a positive Go duration", item.name)
		}
		*item.target = duration
	}

	config.MySQLDSN = strings.TrimSpace(getenv("MYSQL_DSN"))
	if config.MySQLDSN == "" {
		return serverConfig{}, fmt.Errorf("MYSQL_DSN is required")
	}

	return config, nil
}

func newHTTPServer(config serverConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              config.Addr,
		Handler:           handler,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
	}
}

func serveUntilCancelled(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration) error {
	serveResult := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()

	select {
	case err := <-serveResult:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serveResult
	}
}
