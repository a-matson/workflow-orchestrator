//go:build integration

package orchestrator_test

import (
	"context"
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
