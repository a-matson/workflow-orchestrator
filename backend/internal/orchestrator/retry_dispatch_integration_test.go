//go:build integration

package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/scheduler"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func TestRetryNotRedispatchedBySibling(t *testing.T) {
	ctx := context.Background()
	store, rdb := testutil.Env(t)
	orch := orchestrator.NewOrchestrator(store, rdb, &testutil.Recorder{})
	def := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "retry-sibling", MaxParallel: 10,
		Tasks: []models.TaskDefinition{
			// An hour-long delay means any enqueue of A during the test comes from the bug, not a due retry.
			{ID: "a", Name: "A", Type: "generic", RetryPolicy: &models.RetryPolicy{MaxRetries: 1, InitialDelay: time.Hour, MaxDelay: time.Hour, BackoffMultiple: 1}},
			{ID: "b", Name: "B", Type: "generic"},
		}}
	testutil.SaveDef(t, store, def)
	exec, err := orch.StartWorkflow(ctx, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	byDef := map[string]*models.TaskMessage{}
	for _, m := range testutil.Drain(t, rdb, 2) {
		if err := orch.MarkTaskRunning(ctx, m.TaskExecID, "testutil", m.RetryCount); err != nil {
			t.Fatal(err)
		}
		byDef[m.TaskDefinitionID] = m
	}
	if err := orch.ProcessResult(ctx, testutil.Fail(byDef["a"], "boom")); err != nil {
		t.Fatal(err)
	}
	if err := orch.ProcessResult(ctx, testutil.Ok(byDef["b"])); err != nil {
		t.Fatal(err)
	}

	testutil.Never(t, 500*time.Millisecond, func() bool { return testutil.Queued(t, rdb, exec.ID) > 0 })
	if row := testutil.TaskRow(t, store, exec.ID, "a"); row.Status != models.TaskStatusRetrying || row.RetryCount != 1 {
		t.Fatalf("task a = %s/retry %d, want retrying/1", row.Status, row.RetryCount)
	}
}

// A due retry's result must be applied even when the worker's pickup write
// was lost, as it is for a first attempt (TestResultForQueuedRowIsApplied).
func TestDueRetryCompletesWithoutPickup(t *testing.T) {
	ctx := context.Background()
	store, rdb := testutil.Env(t)
	orch := orchestrator.NewOrchestrator(store, rdb, &testutil.Recorder{})
	def := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "due-retry", MaxParallel: 10,
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", RetryPolicy: &models.RetryPolicy{MaxRetries: 1, InitialDelay: 10 * time.Millisecond, MaxDelay: 10 * time.Millisecond, BackoffMultiple: 1}},
		}}
	testutil.SaveDef(t, store, def)
	exec, err := orch.StartWorkflow(ctx, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := testutil.Drain(t, rdb, 1)[0]
	if err := orch.MarkTaskRunning(ctx, first.TaskExecID, "testutil", first.RetryCount); err != nil {
		t.Fatal(err)
	}
	if err := orch.ProcessResult(ctx, testutil.Fail(first, "boom")); err != nil {
		t.Fatal(err)
	}

	pollCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.NewRetryPoller(orch).Run(pollCtx)
	}()
	t.Cleanup(func() { stop(); <-done })

	// The poller ticks every 5s.
	testutil.Eventually(t, 10*time.Second, func() bool { return testutil.Queued(t, rdb, exec.ID) > 0 })
	retry := testutil.Drain(t, rdb, 1)[0]
	if err := orch.ProcessResult(ctx, testutil.Ok(retry)); err != nil {
		t.Fatal(err)
	}

	if row := testutil.TaskRow(t, store, exec.ID, "a"); row.Status != models.TaskStatusCompleted || row.RetryCount != 1 {
		t.Fatalf("task a = %s/retry %d, want completed/1", row.Status, row.RetryCount)
	}
}

// setTaskStatus rewrites a row behind the orchestrator's cache, the way a
// commit whose acknowledgement was lost leaves it.
func setTaskStatus(t *testing.T, store *persistence.Store, execID, defID string, status models.TaskStatus) {
	t.Helper()
	if _, err := store.Pool().Exec(context.Background(),
		`UPDATE task_executions SET status = $3 WHERE workflow_exec_id = $1 AND task_definition_id = $2`,
		execID, defID, string(status)); err != nil {
		t.Fatalf("set task %s status: %v", defID, err)
	}
}

func chainDef() *models.WorkflowDefinition {
	return &models.WorkflowDefinition{ID: uuid.NewString(), Name: "chain", MaxParallel: 10,
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic"},
			{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"}},
		}}
}

// A queued write that committed but reported an error leaves the row queued
// while the cache still says pending; dispatch must enqueue it, not fail the
// transition on every tick.
func TestDispatchEnqueuesRowQueuedBehindCache(t *testing.T) {
	ctx := context.Background()
	store, rdb := testutil.Env(t)
	orch := orchestrator.NewOrchestrator(store, rdb, &testutil.Recorder{})
	def := chainDef()
	testutil.SaveDef(t, store, def)
	exec, err := orch.StartWorkflow(ctx, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := testutil.Drain(t, rdb, 1)[0]
	setTaskStatus(t, store, exec.ID, "b", models.TaskStatusQueued)

	runTask(t, orch, a, testutil.Ok(a))

	if n := testutil.Queued(t, rdb, exec.ID); n != 1 {
		t.Fatalf("%d messages queued for b, want 1", n)
	}
}

// Pins REL-6's order, queued before the push: when the queued write fails,
// no message may exist for the row.
func TestDispatchPushesNothingWhenQueueWriteFails(t *testing.T) {
	ctx := context.Background()
	store, rdb := testutil.Env(t)
	orch := orchestrator.NewOrchestrator(store, rdb, &testutil.Recorder{})
	def := chainDef()
	testutil.SaveDef(t, store, def)
	exec, err := orch.StartWorkflow(ctx, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := testutil.Drain(t, rdb, 1)[0]
	// No pending -> queued transition can match a cancelled row.
	setTaskStatus(t, store, exec.ID, "b", models.TaskStatusCancelled)

	runTask(t, orch, a, testutil.Ok(a))

	if n := testutil.Queued(t, rdb, exec.ID); n != 0 {
		t.Fatalf("%d messages queued for b after its queued write failed, want 0", n)
	}
}

// A failed push moves the row back to pending, where the next dispatch picks
// it up, instead of leaving it queued with no message.
func TestFailedEnqueueRollsBackToPending(t *testing.T) {
	ctx := context.Background()
	store, _ := testutil.Env(t)
	// Nothing listens on port 1, so every Redis call fails.
	dead := persistence.NewRedisClient("127.0.0.1:1", "", 0)
	t.Cleanup(func() {
		if err := dead.Close(); err != nil {
			t.Errorf("close redis client: %v", err)
		}
	})
	orch := orchestrator.NewOrchestrator(store, dead, &testutil.Recorder{})
	def := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "dead-redis", MaxParallel: 10,
		Tasks: []models.TaskDefinition{{ID: "a", Name: "A", Type: "generic"}}}
	testutil.SaveDef(t, store, def)
	exec, err := orch.StartWorkflow(ctx, def, nil)
	if err != nil {
		t.Fatal(err)
	}

	if row := testutil.TaskRow(t, store, exec.ID, "a"); row.Status != models.TaskStatusPending || row.RetryCount != 0 {
		t.Fatalf("task a = %s/retry %d after a failed enqueue, want pending/0", row.Status, row.RetryCount)
	}
}
