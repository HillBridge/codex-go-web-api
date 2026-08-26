package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNewWithoutEndpointUsesNoopProvider(t *testing.T) {
	runtime, err := New(context.Background(), Config{ServiceName: "user-order-api"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.TracerProvider() == nil {
		t.Fatal("TracerProvider() = nil")
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestHTTPHandlerContinuesTraceparentWithNormalizedRoute(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	handler := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), provider)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/42", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Name() != "GET /api/v1/orders/:id" {
		t.Fatalf("span name = %q", spans[0].Name())
	}
	if got := spans[0].Parent().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("parent trace ID = %q", got)
	}
}
