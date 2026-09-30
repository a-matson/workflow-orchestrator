//go:build integration

package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/goleak"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const eventWait = 2 * time.Second

func setupOrchestrator(t *testing.T) (*orchestrator.Orchestrator, *persistence.Store, *persistence.RedisClient, *testutil.Recorder) {
	t.Helper()
	store, redis := testutil.Env(t)
	rec := &testutil.Recorder{}
	return orchestrator.NewOrchestrator(store, redis, rec), store, redis, rec
}

func startWorkflow(t *testing.T, orch *orchestrator.Orchestrator, store *persistence.Store, def *models.WorkflowDefinition) *models.WorkflowExecution {
	t.Helper()
	testutil.SaveDef(t, store, def)
	exec, err := orch.StartWorkflow(context.Background(), def, nil)
	if err != nil {
		t.Fatalf("StartWorkflow: %v", err)
	}
	return exec
}

func hasEvent(rec *testutil.Recorder, typ string) bool {
	for _, ev := range rec.Events() {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func TestOrchestrator_StartWorkflow_LinearDAG(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Linear Test",
		Tasks: []models.TaskDefinition{
			{ID: "step-a", Name: "Step A", Type: "generic", Dependencies: []string{}},
			{ID: "step-b", Name: "Step B", Type: "generic", Dependencies: []string{"step-a"}},
			{ID: "step-c", Name: "Step C", Type: "generic", Dependencies: []string{"step-b"}},
		},
		MaxParallel: 10,
	})

	if exec.ID == "" {
		t.Error("expected non-empty execution ID")
	}
	if exec.Status != models.WorkflowStatusRunning {
		t.Errorf("expected Running, got %s", exec.Status)
	}

	events := rec.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one broadcast event")
	}
	if events[0].Type != models.WSEventWorkflowStarted {
		t.Errorf("expected first event %s, got %s", models.WSEventWorkflowStarted, events[0].Type)
	}

	// Only the root of a linear DAG is ready at start.
	testutil.Eventually(t, eventWait, func() bool { return testutil.Queued(t, redis, exec.ID) == 1 })
	if msg := testutil.Drain(t, redis, 1)[0]; msg.TaskDefinitionID != "step-a" {
		t.Errorf("expected step-a dispatched first, got %s", msg.TaskDefinitionID)
	}
}

func TestOrchestrator_ProcessResult_AdvancesDAG(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Advance Test",
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
			{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"}},
		},
		MaxParallel: 10,
	})

	msgA := testutil.Drain(t, redis, 1)[0]
	if msgA.TaskDefinitionID != "a" {
		t.Fatalf("expected a dispatched first, got %s", msgA.TaskDefinitionID)
	}
	// dispatchReadyTasks persists "queued" only after the enqueue, so a
	// result processed before that write lands is overwritten back to queued.
	// Wait for the write so this test checks DAG advancement, not that race.
	testutil.Eventually(t, eventWait, func() bool {
		return testutil.TaskRow(t, store, exec.ID, "a").Status == models.TaskStatusQueued
	})
	if err := orch.ProcessResult(context.Background(), testutil.Ok(msgA)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	msgB := testutil.Drain(t, redis, 1)[0]
	if msgB.TaskDefinitionID != "b" {
		t.Errorf("expected b dispatched after a completed, got %s", msgB.TaskDefinitionID)
	}
	if got := testutil.TaskRow(t, store, exec.ID, "a").Status; got != models.TaskStatusCompleted {
		t.Errorf("task a status = %s, want %s", got, models.TaskStatusCompleted)
	}
}

func TestOrchestrator_RetryOnFailure(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)

	startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Retry Test",
		Tasks: []models.TaskDefinition{
			{
				ID: "flaky", Name: "Flaky Task", Type: "generic",
				Dependencies: []string{},
				RetryPolicy: &models.RetryPolicy{
					MaxRetries:      3,
					InitialDelay:    100 * time.Millisecond,
					MaxDelay:        5 * time.Second,
					BackoffMultiple: 2,
					Jitter:          false,
				},
			},
		},
		MaxParallel: 10,
	})

	msg := testutil.Drain(t, redis, 1)[0]
	if err := orch.ProcessResult(context.Background(), testutil.Fail(msg, "transient error")); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	testutil.Eventually(t, eventWait, func() bool { return hasEvent(rec, models.WSEventTaskRetrying) })
}

func TestOrchestrator_DeadLetter_MaxRetriesExceeded(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)

	startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "DLQ Test",
		Tasks: []models.TaskDefinition{
			{
				ID: "doomed", Name: "Doomed Task", Type: "generic",
				Dependencies: []string{},
				RetryPolicy:  &models.RetryPolicy{MaxRetries: 0},
			},
		},
		MaxParallel: 10,
	})

	msg := testutil.Drain(t, redis, 1)[0]
	if err := orch.ProcessResult(context.Background(), testutil.Fail(msg, "fatal error")); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	testutil.Eventually(t, eventWait, func() bool { return hasEvent(rec, models.WSEventTaskFailed) })
	testutil.Eventually(t, eventWait, func() bool { return hasEvent(rec, models.WSEventWorkflowFailed) })
}

func TestStartWorkflow_NoPartialRows(t *testing.T) {
	orch, store, _, rec := setupOrchestrator(t)
	ctx := context.Background()

	tasks := []models.TaskDefinition{
		{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
		{ID: "b", Name: "B", Type: "generic", Dependencies: []string{}},
		{ID: "c", Name: "C", Type: "generic", Dependencies: []string{}},
	}
	def := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "Partial Start", Tasks: tasks, MaxParallel: 10}
	// The definition row must exist for the executions FK, but jsonb rejects
	// NUL too, so only the in-memory copy carries the poisoned name.
	testutil.SaveDef(t, store, def)
	poisoned := *def
	poisoned.Tasks = append([]models.TaskDefinition(nil), tasks...)
	// Postgres text rejects NUL, so inserting the third task row fails.
	poisoned.Tasks[2].Name = "C\x00"

	_, err := orch.StartWorkflow(ctx, &poisoned, nil)
	if err == nil || !strings.Contains(err.Error(), "inserting task c") {
		t.Fatalf("StartWorkflow error = %v; want the rejected task c row", err)
	}

	var execs, taskRows int
	if err := store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM workflow_executions WHERE workflow_id = $1`, def.ID).Scan(&execs); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if err := store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM task_executions t JOIN workflow_executions e ON e.id = t.workflow_exec_id
		 WHERE e.workflow_id = $1`, def.ID).Scan(&taskRows); err != nil {
		t.Fatalf("count task executions: %v", err)
	}
	if execs != 0 || taskRows != 0 {
		t.Errorf("failed start left %d execution rows and %d task rows; want 0 and 0", execs, taskRows)
	}
	if m := orch.GetMetrics(); m["active_workflows"] != 0 || m["workflows_started"] != 0 {
		t.Errorf("failed start registered in-memory state: %v", m)
	}
	if hasEvent(rec, models.WSEventWorkflowStarted) {
		t.Error("failed start broadcast workflow_started")
	}
}
