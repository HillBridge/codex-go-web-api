package telemetry

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName      string
	OTLPGRPCEndpoint string
	Insecure         bool
}

type Runtime struct {
	provider trace.TracerProvider
	shutdown func(context.Context) error

	shutdownOnce sync.Once
	shutdownErr  error
}

func New(ctx context.Context, config Config) (*Runtime, error) {
	if config.OTLPGRPCEndpoint == "" {
		return &Runtime{
			provider: trace.NewNoopTracerProvider(),
			shutdown: func(context.Context) error { return nil },
		}, nil
	}

	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(config.OTLPGRPCEndpoint)}
	if config.Insecure {
		options = append(options, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(config.ServiceName))),
	)
	return &Runtime{provider: provider, shutdown: provider.Shutdown}, nil
}

func (r *Runtime) TracerProvider() trace.TracerProvider {
	return r.provider
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	r.shutdownOnce.Do(func() {
		r.shutdownErr = r.shutdown(ctx)
	})
	return r.shutdownErr
}
