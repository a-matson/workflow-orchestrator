//go:build integration

package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func setupStore(t *testing.T) *persistence.Store {
	t.Helper()
	store, _ := testutil.Env(t)
	return store
}

func makeWorkflowDef(name string) *models.WorkflowDefinition {
	return &models.WorkflowDefinition{
		ID:      uuid.NewString(),
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

// createExecution saves def and an execution of it in the given status.
func createExecution(t *testing.T, store *persistence.Store, def *models.WorkflowDefinition, status models.WorkflowStatus) *models.WorkflowExecution {
	t.Helper()
	testutil.SaveDef(t, store, def)
	now := time.Now()
	exec := &models.WorkflowExecution{
		ID:           uuid.NewString(),
		WorkflowID:   def.ID,
		WorkflowName: def.Name,
		Status:       status,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := store.CreateWorkflowExecution(context.Background(), exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	return exec
}

// ── Workflow Definition CRUD ─────────────────────────────────────

func TestStore_SaveAndGetWorkflowDefinition(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("Test Workflow")
	testutil.SaveDef(t, store, def)

	got, err := store.GetWorkflowDefinition(ctx, def.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if got.ID != def.ID {
		t.Errorf("ID mismatch: %s != %s", got.ID, def.ID)
	}
	if got.Name != def.Name {
		t.Errorf("Name mismatch: %s != %s", got.Name, def.Name)
	}
	if len(got.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(got.Tasks))
	}
}

func TestStore_SaveWorkflowDefinition_Upsert(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	def := makeWorkflowDef("Original")
	testutil.SaveDef(t, store, def)

	def.Name = "Updated"
	def.UpdatedAt = time.Now()
	if err := store.SaveWorkflowDefinition(ctx, def); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	got, err := store.GetWorkflowDefinition(ctx, def.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.Name != "Updated" {
		t.Errorf("expected Updated, got %s", got.Name)
	}
}

func TestStore_ListWorkflowDefinitions(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	for range 3 {
		testutil.SaveDef(t, store, makeWorkflowDef("WF"))
	}

	list, err := store.ListWorkflowDefinitions(ctx, 50, 0)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	// The database is fresh per test, so the count is exact.
	if len(list) != 3 {
		t.Errorf("expected 3, got %d", len(list))
	}
}

// ── Workflow Execution CRUD ──────────────────────────────────────

func TestStore_CreateAndGetWorkflowExecution(t *testing.T) {
	store := setupStore(t)

	exec := createExecution(t, store, makeWorkflowDef("Exec WF"), models.WorkflowStatusPending)

	got, err := store.GetWorkflowExecution(context.Background(), exec.ID)
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

	exec := createExecution(t, store, makeWorkflowDef("Update WF"), models.WorkflowStatusPending)

	now := time.Now()
	exec.Status = models.WorkflowStatusCompleted
	exec.StartedAt = &now
	completed := now.Add(5 * time.Second)
	exec.CompletedAt = &completed
	exec.UpdatedAt = completed

	if err := store.UpdateWorkflowExecution(ctx, exec); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, err := store.GetWorkflowExecution(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
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

	def := makeWorkflowDef("Task WF")
	exec := createExecution(t, store, def, models.WorkflowStatusRunning)

	now := time.Now()
	for _, td := range def.Tasks {
		taskExec := &models.TaskExecution{
			ID:               uuid.NewString(),
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

	exec := createExecution(t, store, makeWorkflowDef("Log WF"), models.WorkflowStatusRunning)
	now := time.Now()
	task := &models.TaskExecution{ID: uuid.NewString(), WorkflowExecID: exec.ID, TaskDefinitionID: "t1", TaskName: "Task 1", TaskType: "generic", Status: models.TaskStatusRunning, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTaskExecution(ctx, task); err != nil {
		t.Fatalf("create task failed: %v", err)
	}

	entry := models.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "test log entry",
		Fields:    map[string]any{"key": "value"},
	}

	if err := store.AppendTaskLog(ctx, task.ID, entry); err != nil {
		t.Fatalf("append log failed: %v", err)
	}

	got, err := store.GetTaskExecution(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if len(got.Logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(got.Logs))
	}
	if got.Logs[0].Message != "test log entry" {
		t.Errorf("log message mismatch: %s", got.Logs[0].Message)
	}
}

func TestStore_GetTasksReadyForRetry(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	exec := createExecution(t, store, makeWorkflowDef("Retry WF"), models.WorkflowStatusRunning)
	now := time.Now()
	past := now.Add(-1 * time.Minute)
	task := &models.TaskExecution{
		ID: uuid.NewString(), WorkflowExecID: exec.ID,
		TaskDefinitionID: "t1", TaskName: "T1", TaskType: "generic",
		Status: models.TaskStatusRetrying, RetryCount: 1, MaxRetries: 3,
		NextRetryAt: &past, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTaskExecution(ctx, task); err != nil {
		t.Fatalf("create task failed: %v", err)
	}
	// CreateTaskExecution does not persist next_retry_at; the update does.
	if err := store.UpdateTaskExecution(ctx, task); err != nil {
		t.Fatalf("update task failed: %v", err)
	}

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
