//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/templating"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
	"github.com/a-matson/workflow-orchestrator/backend/internal/tracing"
)

type fakeSecrets map[string]string

func (f fakeSecrets) Secret(_ context.Context, name string) (string, error) { return f[name], nil }

// F3: a secret renders into the task's config in the worker, and its value
// is masked in the log lines and output the run reports.
func TestExecuteTask_SecretRendersAndIsRedacted(t *testing.T) {
	_, redis := testutil.Env(t)
	var gotPath string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`echo ` + strings.TrimPrefix(r.URL.Path, "/")))
	})
	g, err := egress.New(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{id: "worker-test", redis: redis, notifier: pickupNotifier{}, httpClient: g.HTTPClient(5 * time.Second),
		running: newRunningTasks(), secrets: fakeSecrets{"token": "s3cr3t-value"}}
	id := uuid.NewString()
	w.executeTask(context.Background(), &models.TaskMessage{
		TaskExecID: id, WorkflowExecID: uuid.NewString(), TaskType: "http_request",
		Config:       map[string]any{"url": srv.URL + `/{{ secret "token" }}`},
		TemplateData: templating.Data(nil, nil),
	})

	if gotPath != "/s3cr3t-value" {
		t.Fatalf("server saw %q, want the rendered secret", gotPath)
	}
	res, err := redis.DequeueResult(context.Background(), 2*time.Second)
	if err != nil || res == nil {
		t.Fatalf("DequeueResult = %v, %v", res, err)
	}
	logs, err := json.Marshal(res.Logs)
	if err != nil {
		t.Fatal(err)
	}
	for what, b := range map[string]string{"logs": string(logs), "output": string(res.Output)} {
		if strings.Contains(b, "s3cr3t-value") {
			t.Errorf("%s leak the secret: %s", what, b)
		}
	}
	if !strings.Contains(string(logs), "***") {
		t.Errorf("logs carry no redaction marker: %s", logs)
	}
}

// O5: the worker's span continues the trace the dispatch sent in the
// message, and the result carries it on.
func TestExecuteTask_ContinuesTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	_, redis := testutil.Env(t)
	srv, _ := countingServer(t, func(w http.ResponseWriter, _ *http.Request) {})
	g, err := egress.New(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{id: "worker-test", redis: redis, notifier: pickupNotifier{}, httpClient: g.HTTPClient(5 * time.Second),
		running: newRunningTasks()}
	dispatchCtx, dispatch := tracing.Start(context.Background(), "", "dispatch task")
	dispatch.End()
	w.executeTask(context.Background(), &models.TaskMessage{
		TaskExecID: uuid.NewString(), WorkflowExecID: uuid.NewString(), TaskType: "http_request",
		Config: map[string]any{"url": srv.URL}, TraceParent: tracing.TraceParent(dispatchCtx),
	})

	res, err := redis.DequeueResult(context.Background(), 2*time.Second)
	if err != nil || res == nil {
		t.Fatalf("DequeueResult = %v, %v", res, err)
	}
	spans := rec.Ended()
	var run sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "run task" {
			run = s
		}
	}
	if run == nil {
		t.Fatalf("no run task span among %d", len(spans))
	}
	if run.SpanContext().TraceID() != dispatch.SpanContext().TraceID() || run.Parent().SpanID() != dispatch.SpanContext().SpanID() {
		t.Errorf("run span is not a child of the dispatch span")
	}
	if want := tracing.TraceParent(trace.ContextWithSpanContext(context.Background(), run.SpanContext())); res.TraceParent != want {
		t.Errorf("result traceparent = %q, want the run span's %q", res.TraceParent, want)
	}
}
