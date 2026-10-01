package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func record(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func TestSetup_OffWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

// A traceparent carries the span across a queue: the far side's span joins
// the same trace as its child, while a span already in ctx takes precedence.
func TestStart_ContinuesTraceParent(t *testing.T) {
	record(t)
	if got := TraceParent(context.Background()); got != "" {
		t.Errorf("TraceParent without a span = %q, want empty", got)
	}
	rootCtx, root := Start(context.Background(), "", "root")
	tp := TraceParent(rootCtx)

	_, child := Start(context.Background(), tp, "child")
	rs, cs := root.SpanContext(), child.(sdktrace.ReadOnlySpan)
	if cs.SpanContext().TraceID() != rs.TraceID() || cs.Parent().SpanID() != rs.SpanID() {
		t.Errorf("child trace %s parent %s, want trace %s parent %s", cs.SpanContext().TraceID(), cs.Parent().SpanID(), rs.TraceID(), rs.SpanID())
	}

	otherCtx, other := Start(context.Background(), "", "other")
	_, nested := Start(otherCtx, tp, "nested")
	if nested.SpanContext().TraceID() != other.SpanContext().TraceID() {
		t.Errorf("a span in ctx must win over the fallback parent")
	}
	if trace.SpanContextFromContext(otherCtx).TraceID() == rs.TraceID() {
		t.Fatal("two root spans share a trace")
	}
}

func TestNewResource_ServiceName(t *testing.T) {
	serviceName := func() string {
		t.Helper()
		res, err := newResource(context.Background(), "fluxor-backend")
		if err != nil {
			t.Fatal(err)
		}
		v, _ := res.Set().Value("service.name")
		return v.AsString()
	}
	t.Setenv("OTEL_SERVICE_NAME", "")
	if got := serviceName(); got != "fluxor-backend" {
		t.Errorf("service.name = %q, want fluxor-backend", got)
	}
	t.Setenv("OTEL_SERVICE_NAME", "fluxor-staging")
	if got := serviceName(); got != "fluxor-staging" {
		t.Errorf("service.name with OTEL_SERVICE_NAME = %q, want fluxor-staging", got)
	}
}
