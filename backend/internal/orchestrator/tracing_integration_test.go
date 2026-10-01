//go:build integration

package orchestrator_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
	"github.com/a-matson/workflow-orchestrator/backend/internal/tracing"
)

// O5: a triggered run is one trace. The request's span parents the run's
// root, dispatches carry it to the worker in TaskMessage, and processing a
// result rejoins it through TaskResult, including the next dispatch.
func TestTracing_OneTracePerRun(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	orch, store, redis, _ := setupOrchestrator(t)
	def := &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Traced", MaxParallel: 1,
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "a", Type: "generic", Dependencies: []string{}},
			{ID: "b", Name: "b", Type: "generic", Dependencies: []string{"a"}},
		},
	}
	testutil.SaveDef(t, store, def)
	reqCtx, req := tracing.Start(context.Background(), "", "request")
	if _, err := orch.StartWorkflow(reqCtx, def, nil); err != nil {
		t.Fatalf("StartWorkflow: %v", err)
	}
	req.End()
	traceID := req.SpanContext().TraceID()

	for range 2 {
		m := testutil.Drain(t, redis, 1)[0]
		if m.TraceParent == "" || !containsTrace(m.TraceParent, traceID.String()) {
			t.Fatalf("task %s traceparent = %q, want trace %s", m.TaskDefinitionID, m.TraceParent, traceID)
		}
		// As the worker would: its span continues the message's trace.
		workerCtx, run := tracing.Start(context.Background(), m.TraceParent, "run task")
		res := testutil.Ok(m)
		res.TraceParent = tracing.TraceParent(workerCtx)
		run.End()
		runTask(t, orch, m, res)
	}

	names := map[string]int{}
	for _, s := range rec.Ended() {
		if s.SpanContext().TraceID() != traceID {
			t.Errorf("span %q is in trace %s, want %s", s.Name(), s.SpanContext().TraceID(), traceID)
		}
		names[s.Name()]++
	}
	for name, want := range map[string]int{"start workflow": 1, "dispatch task": 2, "run task": 2, "process result": 2} {
		if names[name] != want {
			t.Errorf("%d %q spans, want %d (all: %v)", names[name], name, want, names)
		}
	}
}

func containsTrace(traceparent, traceID string) bool {
	// 00-<trace id>-<span id>-<flags>
	return len(traceparent) > 35 && traceparent[3:35] == traceID
}
