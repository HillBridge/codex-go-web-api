package telemetry

import (
	"net/http"

	"bridge-go/user-order-api/internal/platform/observability"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTPHandler instruments inbound requests while keeping span names bounded to
// the same normalized route templates used by Prometheus metrics.
func HTTPHandler(next http.Handler, provider trace.TracerProvider) http.Handler {
	if provider == nil {
		provider = trace.NewNoopTracerProvider()
	}
	return otelhttp.NewHandler(next, "HTTP",
		otelhttp.WithTracerProvider(provider),
		otelhttp.WithPropagators(propagation.TraceContext{}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + observability.NormalizeRoute(r.URL.Path)
		}),
	)
}
