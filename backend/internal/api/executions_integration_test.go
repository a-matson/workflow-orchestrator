//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func TestAPI_ListExecutions_HasTasks(t *testing.T) {
	store, redis := testutil.Env(t)
	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	def := &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "List Tasks",
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "Task A", Type: "generic", Dependencies: []string{}},
			{ID: "b", Name: "Task B", Type: "generic", Dependencies: []string{"a"}},
		},
		MaxParallel: 10,
	}
	testutil.SaveDef(t, store, def)
	if _, err := orch.StartWorkflow(context.Background(), def, nil); err != nil {
		t.Fatalf("StartWorkflow: %v", err)
	}

	h := api.NewHandlerWithStorage(store, redis, orch, api.NewHub(), nil).Routes()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/executions", nil)
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}

	var body struct {
		Executions []models.WorkflowExecution `json:"executions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Executions) != 1 {
		t.Fatalf("got %d executions, want 1", len(body.Executions))
	}
	tasks := body.Executions[0].Tasks
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	for _, task := range tasks {
		if task.ID == "" || task.Status == "" || task.TaskName == "" {
			t.Errorf("task missing id/status/task_name: %+v", task)
		}
	}
}
