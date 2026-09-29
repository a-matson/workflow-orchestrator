//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func TestAPI_ListExecutions_HasTasks(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	newDef := func(name string) *models.WorkflowDefinition {
		def := &models.WorkflowDefinition{
			ID:   uuid.NewString(),
			Name: name,
			Tasks: []models.TaskDefinition{
				{ID: "a", Name: name + " A", Type: "generic", Dependencies: []string{}},
				{ID: "b", Name: name + " B", Type: "generic", Dependencies: []string{"a"}},
			},
			MaxParallel: 10,
		}
		testutil.SaveDef(t, store, def)
		return def
	}
	defOne, defTwo := newDef("One"), newDef("Two")
	one, err := orch.StartWorkflow(ctx, defOne, nil)
	if err != nil {
		t.Fatalf("StartWorkflow one: %v", err)
	}
	two, err := orch.StartWorkflow(ctx, defTwo, nil)
	if err != nil {
		t.Fatalf("StartWorkflow two: %v", err)
	}
	now := time.Now()
	empty := &models.WorkflowExecution{
		ID: uuid.NewString(), WorkflowID: defOne.ID, WorkflowName: "Empty",
		Status: models.WorkflowStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateWorkflowExecution(ctx, empty); err != nil {
		t.Fatalf("create empty execution: %v", err)
	}

	// Give a task heavy columns; the list must not return them.
	stored, err := store.ListTaskExecutions(ctx, one.ID)
	if err != nil || len(stored) == 0 {
		t.Fatalf("list tasks of first execution: %v (%d)", err, len(stored))
	}
	stored[0].Output = json.RawMessage(`{"big":"payload"}`)
	stored[0].Logs = []models.LogEntry{{Timestamp: now, Level: "info", Message: "hello"}}
	if err := store.UpdateTaskExecution(ctx, stored[0]); err != nil {
		t.Fatalf("update task: %v", err)
	}

	h := api.NewHandler(store, redis, orch, api.NewHub(), nil).Routes()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/executions", nil)
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
	if len(body.Executions) != 3 {
		t.Fatalf("got %d executions, want 3", len(body.Executions))
	}
	byID := map[string]models.WorkflowExecution{}
	for _, e := range body.Executions {
		byID[e.ID] = e
	}

	for _, tc := range []struct {
		id, name string
	}{{one.ID, "One"}, {two.ID, "Two"}} {
		tasks := byID[tc.id].Tasks
		if len(tasks) != 2 {
			t.Fatalf("%s: got %d tasks, want 2", tc.name, len(tasks))
		}
		// StartWorkflow stamps every task with one created_at, so only the
		// (created_at, id) ordering contract is checked, not A-before-B.
		names := map[string]bool{}
		for i, task := range tasks {
			names[task.TaskName] = true
			if task.ID == "" || task.Status == "" {
				t.Errorf("%s task %d missing id/status: %+v", tc.name, i, task)
			}
			if task.WorkflowExecID != tc.id {
				t.Errorf("%s task %d belongs to %s", tc.name, i, task.WorkflowExecID)
			}
			if len(task.Output) != 0 || len(task.Logs) != 0 {
				t.Errorf("%s task %d: output/logs must be omitted, got %s / %d logs", tc.name, i, task.Output, len(task.Logs))
			}
			if i > 0 {
				prev := tasks[i-1]
				if task.CreatedAt.Before(prev.CreatedAt) || (task.CreatedAt.Equal(prev.CreatedAt) && task.ID < prev.ID) {
					t.Errorf("%s tasks not sorted by (created_at, id): %s before %s", tc.name, prev.ID, task.ID)
				}
			}
		}
		if !names[tc.name+" A"] || !names[tc.name+" B"] {
			t.Errorf("%s: task names = %v, want A and B", tc.name, names)
		}
	}

	// Decode raw so nil and [] are distinguishable.
	var raw struct {
		Executions []struct {
			ID    string          `json:"id"`
			Tasks json.RawMessage `json:"tasks"`
		} `json:"executions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, e := range raw.Executions {
		if e.ID == empty.ID && string(e.Tasks) != "[]" {
			t.Errorf("task-less execution: tasks = %s, want []", e.Tasks)
		}
	}
}
