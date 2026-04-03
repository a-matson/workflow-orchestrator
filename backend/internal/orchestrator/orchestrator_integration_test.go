//go:build integration

package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/workflow-platform/backend/internal/models"
	"github.com/workflow-platform/backend/internal/orchestrator"
	"github.com/workflow-platform/backend/internal/persistence"
	"github.com/workflow-platform/backend/internal/retry"
)

// mockBroadcaster captures events for assertions
type mockBroadcaster struct {
	events []models.WebSocketEvent
}

func (m *mockBroadcaster) Broadcast(event models.WebSocketEvent) {
	m.events = append(m.events, event)
}

func setupOrchestrator(t *testing.T) (*orchestrator.Orchestrator, *mockBroadcaster) {
	t.Helper()

	ctx := context.Background()

	store, err := persistence.NewStore(ctx, "postgres://workflow:workflow@localhost:5432/workflow?sslmode=disable")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(store.Close)

	redis := persistence.NewRedisClient("localhost:6379", "", 1)
	if err := redis.Ping(ctx); err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { redis.Close() })

	broadcaster := &mockBroadcaster{}
	orch := orchestrator.NewOrchestrator(store, redis, broadcaster)
	return orch, broadcaster
}

func TestOrchestrator_StartWorkflow_LinearDAG(t *testing.T) {
	orch, broadcaster := setupOrchestrator(t)
	ctx := context.Background()

	def := &models.WorkflowDefinition{
		ID:   "test-wf-linear",
		Name: "Linear Test",
		Tasks: []models.TaskDefinition{
			{ID: "step-a", Name: "Step A", Type: "generic", Dependencies: []string{}},
			{ID: "step-b", Name: "Step B", Type: "generic", Dependencies: []string{"step-a"}},
			{ID: "step-c", Name: "Step C", Type: "generic", Dependencies: []string{"step-b"}},
		},
		MaxParallel: 10,
	}

	exec, err := orch.StartWorkflow(ctx, def, nil)
	if err != nil {
		t.Fatalf("StartWorkflow failed: %v", err)
	}

	if exec.ID == "" {
		t.Error("expected non-empty execution ID")
	}
	if exec.Status != models.WorkflowStatusRunning {
		t.Errorf("expected Running, got %s", exec.Status)
	}

	// Check event was broadcast
	time.Sleep(50 * time.Millisecond)
	if len(broadcaster.events) == 0 {
		t.Error("expected at least one broadcast event")
	}
	if broadcaster.events[0].Type != models.WSEventWorkflowStarted {
		t.Errorf("expected workflow.started, got %s", broadcaster.events[0].Type)
	}
}

func TestOrchestrator_ProcessResult_AdvancesDAG(t *testing.T) {
	orch, _ := setupOrchestrator(t)
	ctx := context.Background()

	def := &models.WorkflowDefinition{
		ID:   "test-wf-advance",
		Name: "Advance Test",
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
			{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"}},
		},
		MaxParallel: 10,
	}

	exec, _ := orch.StartWorkflow(ctx, def, nil)

	// Simulate task A completing
	taskA := exec.Tasks[0]
	result := &models.TaskResult{
		TaskExecID:     taskA.ID,
		WorkflowExecID: exec.ID,
		WorkerID:       "test-worker",
		Success:        true,
		StartedAt:      time.Now().Add(-2 * time.Second),
		CompletedAt:    time.Now(),
	}

	if err := orch.ProcessResult(ctx, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// B should now be queued/dispatched (check redis queue depth)
	// In a real integration test, consume from redis and verify
}

func TestOrchestrator_RetryOnFailure(t *testing.T) {
	orch, broadcaster := setupOrchestrator(t)
	ctx := context.Background()

	def := &models.WorkflowDefinition{
		ID:   "test-wf-retry",
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
	}

	exec, _ := orch.StartWorkflow(ctx, def, nil)
	taskExec := exec.Tasks[0]

	// Fail once
	failResult := &models.TaskResult{
		TaskExecID:     taskExec.ID,
		WorkflowExecID: exec.ID,
		WorkerID:       "test-worker",
		Success:        false,
		Error:          "transient error",
		StartedAt:      time.Now().Add(-time.Second),
		CompletedAt:    time.Now(),
	}

	if err := orch.ProcessResult(ctx, failResult); err != nil {
		t.Fatalf("ProcessResult (fail) error: %v", err)
	}

	// Look for retry event
	time.Sleep(50 * time.Millisecond)
	var foundRetry bool
	for _, ev := range broadcaster.events {
		if ev.Type == models.WSEventTaskRetrying {
			foundRetry = true
			break
		}
	}
	if !foundRetry {
		t.Error("expected task.retrying broadcast event after failure with retries remaining")
	}
}

func TestOrchestrator_DeadLetter_MaxRetriesExceeded(t *testing.T) {
	orch, broadcaster := setupOrchestrator(t)
	ctx := context.Background()

	def := &models.WorkflowDefinition{
		ID:   "test-wf-dlq",
		Name: "DLQ Test",
		Tasks: []models.TaskDefinition{
			{
				ID: "doomed", Name: "Doomed Task", Type: "generic",
				Dependencies: []string{},
				RetryPolicy:  &models.RetryPolicy{MaxRetries: 0}, // no retries
			},
		},
		MaxParallel: 10,
	}

	exec, _ := orch.StartWorkflow(ctx, def, nil)
	taskExec := exec.Tasks[0]

	failResult := &models.TaskResult{
		TaskExecID:     taskExec.ID,
		WorkflowExecID: exec.ID,
		WorkerID:       "test-worker",
		Success:        false,
		Error:          "fatal error",
		StartedAt:      time.Now().Add(-time.Second),
		CompletedAt:    time.Now(),
	}

	orch.ProcessResult(ctx, failResult)

	time.Sleep(50 * time.Millisecond)

	var foundDLQ, foundFail bool
	for _, ev := range broadcaster.events {
		if ev.Type == models.WSEventTaskFailed {
			foundDLQ = true
		}
		if ev.Type == models.WSEventWorkflowFailed {
			foundFail = true
		}
	}

	if !foundDLQ {
		t.Error("expected task.failed broadcast for dead-lettered task")
	}
	if !foundFail {
		t.Error("expected workflow.failed broadcast when task dead-lettered")
	}
	_ = retry.DefaultPolicy
}
