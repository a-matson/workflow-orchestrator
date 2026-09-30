//go:build integration

package persistence_test

import (
	"context"
	"fmt"
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

func TestStore_ListWorkflowDefinitions_Paging(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	for i := range 5 {
		def := makeWorkflowDef(fmt.Sprintf("WF%d", i))
		def.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		testutil.SaveDef(t, store, def)
	}

	page, err := store.ListWorkflowDefinitions(ctx, 2, 2)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	// Newest first is WF4..WF0, so offset 2 limit 2 is the 3rd and 4th newest.
	if len(page) != 2 || page[0].Name != "WF2" || page[1].Name != "WF1" {
		t.Errorf("got %v, want [WF2 WF1]", names(page))
	}
}

func names(defs []*models.WorkflowDefinition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
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

// An execution created before definition snapshots existed has none, so it
// resumes with the stored definition.
func TestGetExecutionDefinition_FallsBackWithoutSnapshot(t *testing.T) {
	store := setupStore(t)
	def := makeWorkflowDef("legacy")
	exec := createExecution(t, store, def, models.WorkflowStatusRunning)

	got, err := store.GetExecutionDefinition(context.Background(), exec.ID)
	if err != nil {
		t.Fatalf("GetExecutionDefinition: %v", err)
	}
	if got.ID != def.ID || len(got.Tasks) != len(def.Tasks) {
		t.Errorf("definition = %s with %d tasks, want %s with %d", got.ID, len(got.Tasks), def.ID, len(def.Tasks))
	}
}

// REL-23: global_retry must survive a save and reload, or runs started from
// the stored definition silently fall back to the default policy.
func TestGlobalRetryPersisted(t *testing.T) {
	store := setupStore(t)
	def := makeWorkflowDef("global retry")
	def.GlobalRetry = &models.RetryPolicy{MaxRetries: 7, InitialDelay: time.Second, MaxDelay: time.Minute, BackoffMultiple: 3}
	testutil.SaveDef(t, store, def)

	got, err := store.GetWorkflowDefinition(context.Background(), def.ID)
	if err != nil {
		t.Fatalf("GetWorkflowDefinition: %v", err)
	}
	if got.GlobalRetry == nil || *got.GlobalRetry != *def.GlobalRetry {
		t.Errorf("GlobalRetry = %+v, want %+v", got.GlobalRetry, def.GlobalRetry)
	}
}
