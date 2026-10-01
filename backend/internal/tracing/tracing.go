// Package tracing sets up OpenTelemetry tracing and carries a run's trace
// across the Redis queues as a W3C traceparent string.
package tracing

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentation = "github.com/a-matson/workflow-orchestrator/backend"

// Setup installs an OTLP/gRPC tracer provider, configured by the standard
// OTEL_* environment variables, when OTEL_EXPORTER_OTLP_ENDPOINT is set.
// Unset, tracing stays a no-op. The returned function flushes and stops it.
func Setup(ctx context.Context, service string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	res, err := newResource(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("trace resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// newResource names the service, letting OTEL_SERVICE_NAME and
// OTEL_RESOURCE_ATTRIBUTES override it. Not resource.Merge with
// resource.Default: its own service.name ("unknown_service:<binary>") would win.
func newResource(ctx context.Context, service string) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(service)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(), // last, so the environment wins
	)
}

// TraceParent returns the span in ctx as a W3C traceparent, or "" when ctx
// has no recording span.
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// Start begins a span named name. Its parent is the span in ctx when there is
// one, else parent (a traceparent, as from TraceParent), else none. ctx keeps
// its values and cancellation.
func Start(ctx context.Context, parent, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if !trace.SpanContextFromContext(ctx).IsValid() && parent != "" {
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent})
	}
	// Looked up per call: a tracer kept from before a provider swap stays bound
	// to the first provider ever installed. Providers cache their tracers.
	return otel.Tracer(instrumentation).Start(ctx, name, trace.WithAttributes(attrs...))
}
