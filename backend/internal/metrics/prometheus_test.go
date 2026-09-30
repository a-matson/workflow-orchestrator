package metrics_test

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/a-matson/workflow-orchestrator/backend/internal/metrics"
)

// OBS-1: /metrics must serve the registry the app registers on, and its
// series must carry the orchestrator's values at scrape time.
func TestHandlerServesAppSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	durations := metrics.Register(reg, metrics.Sources{
		Snapshot: func() map[string]int64 {
			return map[string]int64{"workflows_started": 3, "tasks_dead_lettered": 1, "active_workflows": 2}
		},
		QueueDepth: func(context.Context) (int64, error) { return 7, nil },
		WSClients:  func() int { return 4 },
	})
	durations.Observe(0.5)

	rec := httptest.NewRecorder()
	metrics.Handler(reg).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"workflow_executions_started_total 3",
		"tasks_dead_lettered_total 1",
		"active_workflow_executions 2",
		"task_queue_depth 7",
		"websocket_clients_connected 4",
		"task_duration_seconds_count 1",
	} {
		if !strings.Contains(string(body), want+"\n") {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}
