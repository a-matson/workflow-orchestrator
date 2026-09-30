//go:build integration

package worker

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// REL-8: a Redis task lock left by a process that died mid-task must not
// stop the redelivered message from running.
func TestExecuteTask_RunsDespiteLeftoverTaskLock(t *testing.T) {
	_, redis := testutil.Env(t)
	srv, hits := countingServer(t, func(http.ResponseWriter, *http.Request) {})
	g, err := egress.New(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	// Written by key, not through the client, so the test still stands for
	// locks an older version left behind once the lock API is gone.
	if err := testutil.Raw(t, redis).Set(context.Background(), "workflow:task:lock:"+id, "locked", 10*time.Minute).Err(); err != nil {
		t.Fatalf("set leftover lock: %v", err)
	}
	w := &Worker{id: "worker-test", redis: redis, notifier: pickupNotifier{}, httpClient: g.HTTPClient(5 * time.Second), running: newRunningTasks()}

	w.executeTask(context.Background(), &models.TaskMessage{
		TaskExecID: id, WorkflowExecID: uuid.NewString(), TaskType: "http_request",
		Config: map[string]any{"url": srv.URL},
	})

	if n := hits.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}
