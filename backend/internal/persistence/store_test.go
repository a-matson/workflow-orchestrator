//go:build integration

package persistence_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

func setupStore(t *testing.T) *persistence.Store {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://workflow:workflow@localhost:5432/workflow_test?sslmode=disable"
	}
	store, err := persistence.NewStore(context.Background(), dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func makeWorkflowDef(id, name string) *models.WorkflowDefinition {
	return &models.WorkflowDefinition{
		ID:      id,
		Name:    name,
		Version: "1.0.0",
		Tasks: []models.TaskDefinition{
			{ID: "t1", Name: "Task 1", Type: "generic", Dependencies: []string{}},
			{ID: "t2", Name: "Task 2", Type: "http_request", Dependencies: []string{"t1"}},
		},
		MaxParallel: 5,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

// ── Workflow Definition CRUD ─────────────────────────────────────

func TestStore_SaveAndGetWorkflowDefinition(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("test-wf-"+t.Name(), "Test Workflow")

	if err := store.SaveWorkflowDefinition(ctx, def); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	got, err := store.GetWorkflowDefinition(ctx, def.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if got.ID != def.ID {
		t.Errorf("ID mismatch: %s != %s", got.ID, def.ID)
	}
	if got.Name != def.Name {
		t.Errorf("Name mismatch")
	}
	if len(got.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(got.Tasks))
	}
}

func TestStore_SaveWorkflowDefinition_Upsert(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("test-upsert-"+t.Name(), "Original")
	store.SaveWorkflowDefinition(ctx, def)

	def.Name = "Updated"
	def.UpdatedAt = time.Now()
	if err := store.SaveWorkflowDefinition(ctx, def); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	got, _ := store.GetWorkflowDefinition(ctx, def.ID)
	if got.Name != "Updated" {
		t.Errorf("expected Updated, got %s", got.Name)
	}
}

func TestStore_ListWorkflowDefinitions(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		def := makeWorkflowDef("list-test-"+t.Name()+"-"+string(rune('0'+i)), "WF")
		store.SaveWorkflowDefinition(ctx, def)
	}

	list, err := store.ListWorkflowDefinitions(ctx)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(list) < 3 {
		t.Errorf("expected at least 3, got %d", len(list))
	}
}

// ── Workflow Execution CRUD ──────────────────────────────────────

func TestStore_CreateAndGetWorkflowExecution(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("exec-wf-"+t.Name(), "Exec WF")
	store.SaveWorkflowDefinition(ctx, def)

	now := time.Now()
	exec := &models.WorkflowExecution{
		ID:           "exec-" + t.Name(),
		WorkflowID:   def.ID,
		WorkflowName: def.Name,
		Status:       models.WorkflowStatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := store.CreateWorkflowExecution(ctx, exec); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	got, err := store.GetWorkflowExecution(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.Status != models.WorkflowStatusPending {
		t.Errorf("expected Pending, got %s", got.Status)
	}
}

func TestStore_UpdateWorkflowExecution(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("upd-wf-"+t.Name(), "Update WF")
	store.SaveWorkflowDefinition(ctx, def)

	now := time.Now()
	exec := &models.WorkflowExecution{
		ID: "exec-upd-" + t.Name(), WorkflowID: def.ID,
		WorkflowName: def.Name, Status: models.WorkflowStatusPending,
		CreatedAt: now, UpdatedAt: now,
	}
	store.CreateWorkflowExecution(ctx, exec)

	exec.Status = models.WorkflowStatusCompleted
	exec.StartedAt = &now
	completed := now.Add(5 * time.Second)
	exec.CompletedAt = &completed
	exec.UpdatedAt = completed

	if err := store.UpdateWorkflowExecution(ctx, exec); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, _ := store.GetWorkflowExecution(ctx, exec.ID)
	if got.Status != models.WorkflowStatusCompleted {
		t.Errorf("expected Completed, got %s", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

// ── Task Execution CRUD ──────────────────────────────────────────

func TestStore_CreateAndListTaskExecutions(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("task-wf-"+t.Name(), "Task WF")
	store.SaveWorkflowDefinition(ctx, def)

	now := time.Now()
	exec := &models.WorkflowExecution{
		ID: "exec-task-" + t.Name(), WorkflowID: def.ID,
		WorkflowName: def.Name, Status: models.WorkflowStatusRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	store.CreateWorkflowExecution(ctx, exec)

	for i, td := range def.Tasks {
		taskExec := &models.TaskExecution{
			ID:               "task-" + t.Name() + string(rune('a'+i)),
			WorkflowExecID:   exec.ID,
			TaskDefinitionID: td.ID,
			TaskName:         td.Name,
			TaskType:         td.Type,
			Status:           models.TaskStatusPending,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := store.CreateTaskExecution(ctx, taskExec); err != nil {
			t.Fatalf("create task failed: %v", err)
		}
	}

	tasks, err := store.ListTaskExecutions(ctx, exec.ID)
	if err != nil {
		t.Fatalf("list tasks failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestStore_AppendTaskLog(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("log-wf-"+t.Name(), "Log WF")
	store.SaveWorkflowDefinition(ctx, def)
	now := time.Now()
	exec := &models.WorkflowExecution{ID: "exec-log-" + t.Name(), WorkflowID: def.ID, WorkflowName: def.Name, Status: models.WorkflowStatusRunning, CreatedAt: now, UpdatedAt: now}
	store.CreateWorkflowExecution(ctx, exec)
	task := &models.TaskExecution{ID: "task-log-" + t.Name(), WorkflowExecID: exec.ID, TaskDefinitionID: "t1", TaskName: "Task 1", TaskType: "generic", Status: models.TaskStatusRunning, CreatedAt: now, UpdatedAt: now}
	store.CreateTaskExecution(ctx, task)

	entry := models.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "test log entry",
		Fields:    map[string]any{"key": "value"},
	}

	if err := store.AppendTaskLog(ctx, task.ID, entry); err != nil {
		t.Fatalf("append log failed: %v", err)
	}

	got, _ := store.GetTaskExecution(ctx, task.ID)
	if len(got.Logs) != 1 {
		t.Errorf("expected 1 log, got %d", len(got.Logs))
	}
	if got.Logs[0].Message != "test log entry" {
		t.Errorf("log message mismatch: %s", got.Logs[0].Message)
	}
	_ = json.Marshal // suppress unused import
}

func TestStore_GetTasksReadyForRetry(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("retry-wf-"+t.Name(), "Retry WF")
	store.SaveWorkflowDefinition(ctx, def)
	now := time.Now()
	exec := &models.WorkflowExecution{ID: "exec-retry-" + t.Name(), WorkflowID: def.ID, WorkflowName: def.Name, Status: models.WorkflowStatusRunning, CreatedAt: now, UpdatedAt: now}
	store.CreateWorkflowExecution(ctx, exec)

	past := now.Add(-1 * time.Minute)
	task := &models.TaskExecution{
		ID: "task-retry-" + t.Name(), WorkflowExecID: exec.ID,
		TaskDefinitionID: "t1", TaskName: "T1", TaskType: "generic",
		Status: models.TaskStatusRetrying, RetryCount: 1, MaxRetries: 3,
		NextRetryAt: &past, CreatedAt: now, UpdatedAt: now,
	}
	store.CreateTaskExecution(ctx, task)
	store.UpdateTaskExecution(ctx, task)

	ready, err := store.GetTasksReadyForRetry(ctx)
	if err != nil {
		t.Fatalf("get retry tasks failed: %v", err)
	}
	found := false
	for _, r := range ready {
		if r.ID == task.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected task to appear in retry queue")
	}
}
