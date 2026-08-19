package main

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestLoadConfigUsesDefaultPort(t *testing.T) {
	config, err := loadConfig(testEnvironment(nil))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.Addr != ":8888" {
		t.Fatalf("Addr = %q, want %q", config.Addr, ":8888")
	}
}

func TestLoadConfigUsesConfiguredPort(t *testing.T) {
	config, err := loadConfig(testEnvironment(map[string]string{"PORT": "9090"}))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.Addr != ":9090" {
		t.Fatalf("Addr = %q, want %q", config.Addr, ":9090")
	}
}

func TestLoadConfigUsesMySQLDSN(t *testing.T) {
	wantDSN := "app:secret@tcp(localhost:3307)/user_order_api?parseTime=true&loc=UTC"
	config, err := loadConfig(func(key string) string {
		if key == "MYSQL_DSN" {
			return wantDSN
		}
		return ""
	})
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if config.MySQLDSN != wantDSN {
		t.Fatalf("MySQLDSN = %q, want %q", config.MySQLDSN, wantDSN)
	}
}

func TestLoadConfigRejectsMissingMySQLDSN(t *testing.T) {
	_, err := loadConfig(func(string) string { return "" })
	if err == nil || err.Error() != "MYSQL_DSN is required" {
		t.Fatalf("loadConfig() error = %v, want %q", err, "MYSQL_DSN is required")
	}
}

func TestLoadConfigRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "65536", "not-a-port"} {
		t.Run(port, func(t *testing.T) {
			_, err := loadConfig(func(key string) string {
				if key == "PORT" {
					return port
				}
				return ""
			})
			if err == nil {
				t.Fatal("loadConfig() error = nil, want invalid port error")
			}
		})
	}
}

func TestLoadConfigUsesConfiguredTimeouts(t *testing.T) {
	environment := map[string]string{
		"READ_HEADER_TIMEOUT": "1s",
		"READ_TIMEOUT":        "2s",
		"WRITE_TIMEOUT":       "3s",
		"IDLE_TIMEOUT":        "4s",
		"SHUTDOWN_TIMEOUT":    "5s",
	}

	config, err := loadConfig(testEnvironment(environment))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.ReadHeaderTimeout != time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want %v", config.ReadHeaderTimeout, time.Second)
	}
	if config.ReadTimeout != 2*time.Second {
		t.Errorf("ReadTimeout = %v, want %v", config.ReadTimeout, 2*time.Second)
	}
	if config.WriteTimeout != 3*time.Second {
		t.Errorf("WriteTimeout = %v, want %v", config.WriteTimeout, 3*time.Second)
	}
	if config.IdleTimeout != 4*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", config.IdleTimeout, 4*time.Second)
	}
	if config.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", config.ShutdownTimeout, 5*time.Second)
	}
}

func TestLoadConfigRejectsInvalidTimeout(t *testing.T) {
	_, err := loadConfig(func(key string) string {
		if key == "WRITE_TIMEOUT" {
			return "0s"
		}
		return ""
	})
	if err == nil {
		t.Fatal("loadConfig() error = nil, want invalid timeout error")
	}
}

func TestNewHTTPServerAppliesConfiguredTimeouts(t *testing.T) {
	config := serverConfig{
		Addr:              ":9090",
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	server := newHTTPServer(config, handler)

	if server.Addr != config.Addr {
		t.Errorf("Addr = %q, want %q", server.Addr, config.Addr)
	}
	if server.ReadHeaderTimeout != config.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, config.ReadHeaderTimeout)
	}
	if server.ReadTimeout != config.ReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", server.ReadTimeout, config.ReadTimeout)
	}
	if server.WriteTimeout != config.WriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", server.WriteTimeout, config.WriteTimeout)
	}
	if server.IdleTimeout != config.IdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", server.IdleTimeout, config.IdleTimeout)
	}
}

func TestServeUntilCancelledShutsDownServer(t *testing.T) {
	listener := newBlockingListener()
	defer listener.Close()

	server := newHTTPServer(serverConfig{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveUntilCancelled(ctx, server, listener, time.Second)
	}()

	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not begin accepting connections")
	}

	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serveUntilCancelled() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveUntilCancelled() did not return after context cancellation")
	}
}

type blockingListener struct {
	accepted     chan struct{}
	closed       chan struct{}
	acceptOnce   sync.Once
	shutdownOnce sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{
		accepted: make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() { close(l.accepted) })
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	l.shutdownOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *blockingListener) Addr() net.Addr {
	return testAddr("listener")
}

type testAddr string

func (a testAddr) Network() string { return string(a) }

func (a testAddr) String() string { return string(a) }

func testEnvironment(values map[string]string) func(string) string {
	return func(key string) string {
		if key == "MYSQL_DSN" {
			return "app:test@tcp(localhost:3307)/user_order_api?parseTime=true&loc=UTC"
		}
		return values[key]
	}
}
