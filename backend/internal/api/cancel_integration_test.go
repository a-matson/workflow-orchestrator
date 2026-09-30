//go:build integration

package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// cancelDuringRun starts a workflow of tasks, marks its root task "a"
// running, cancels the execution through the authenticated API, and then
// delivers a's success, as a worker finishing after the cancel would. It
// returns the cancel call so the caller can issue more.
func cancelDuringRun(t *testing.T, tasks []models.TaskDefinition) (*persistence.Store, *persistence.RedisClient, string, func() *httptest.ResponseRecorder) {
	t.Helper()
	store, redis := testutil.Env(t)
	ctx := context.Background()
	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	srv := api.NewHandler(store, redis, orch, api.NewHub(), nil).Server(nil)
	operator, _, err := store.CreateAPIKey(ctx, "operator", "operator")
	if err != nil {
		t.Fatalf("create operator key: %v", err)
	}

	def := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "cancel", Tasks: tasks, MaxParallel: 10}
	testutil.SaveDef(t, store, def)
	exec, err := orch.StartWorkflow(ctx, def, nil)
	if err != nil {
		t.Fatalf("StartWorkflow: %v", err)
	}
	msgA := testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, msgA.TaskExecID, "testutil", msgA.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}

	cancel := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/executions/"+exec.ID+"/cancel", nil)
		req.Host = "localhost"
		req.Header.Set("Authorization", "Bearer "+operator)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	if rr := cancel(); rr.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200: %s", rr.Code, rr.Body)
	}
	if err := orch.ProcessResult(ctx, testutil.Ok(msgA)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	return store, redis, exec.ID, cancel
}

func execStatus(t *testing.T, store *persistence.Store, id string) models.WorkflowStatus {
	t.Helper()
	exec, err := store.GetWorkflowExecution(context.Background(), id)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	return exec.Status
}

// REL-7: cancel wrote only the execution row, so a's late result still
// dispatched b.
func TestCancel_StopsDependents(t *testing.T) {
	store, redis, execID, _ := cancelDuringRun(t, []models.TaskDefinition{
		{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
		{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"}},
	})

	testutil.Never(t, 300*time.Millisecond, func() bool { return testutil.Queued(t, redis, execID) > 0 })
	for _, id := range []string{"a", "b"} {
		if got := testutil.TaskRow(t, store, execID, id).Status; got != models.TaskStatusCancelled {
			t.Errorf("task %s status = %s, want %s", id, got, models.TaskStatusCancelled)
		}
	}
	if got := execStatus(t, store, execID); got != models.WorkflowStatusCancelled {
		t.Errorf("execution status = %s, want %s", got, models.WorkflowStatusCancelled)
	}
}

// REL-7: completeWorkflow rewrote the whole execution row, turning a
// cancelled execution into a completed one.
func TestCancel_NotOverwrittenByCompletion(t *testing.T) {
	store, _, execID, cancel := cancelDuringRun(t, []models.TaskDefinition{
		{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
	})

	if got := execStatus(t, store, execID); got != models.WorkflowStatusCancelled {
		t.Errorf("execution status = %s, want %s", got, models.WorkflowStatusCancelled)
	}
	if rr := cancel(); rr.Code != http.StatusConflict {
		t.Errorf("second cancel = %d, want 409: %s", rr.Code, rr.Body)
	}
}
