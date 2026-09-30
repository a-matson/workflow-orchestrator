//go:build integration

package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
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
	runTask(t, orch, store, msgA, testutil.Ok(msgA))

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
	runTask(t, orch, store, msg, testutil.Fail(msg, "transient error"))

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
	runTask(t, orch, store, msg, testutil.Fail(msg, "fatal error"))

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

// runTask takes msg through the worker's pickup and returns once the
// orchestrator has accepted res. It waits for the queued write first because
// a worker only receives a message after dispatch enqueued it.
func runTask(t *testing.T, orch *orchestrator.Orchestrator, store *persistence.Store, msg *models.TaskMessage, res *models.TaskResult) {
	t.Helper()
	ctx := context.Background()
	testutil.Eventually(t, eventWait, func() bool {
		return testutil.TaskRow(t, store, msg.WorkflowExecID, msg.TaskDefinitionID).Status == models.TaskStatusQueued
	})
	if err := orch.MarkTaskRunning(ctx, msg.TaskExecID, "testutil"); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}
	if err := orch.ProcessResult(ctx, res); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
}

func TestArtifactsFlowToDependents(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)

	startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Artifact Flow",
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", Dependencies: []string{},
				ArtifactsOut: []models.ArtifactRef{{Path: "out.txt"}}},
			{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"},
				ArtifactsIn: []models.ArtifactRef{{Path: "out.txt"}}},
		},
		MaxParallel: 10,
	})

	msgA := testutil.Drain(t, redis, 1)[0]
	produced := models.ResolvedArtifact{Path: "out.txt", MinioKey: "artifacts/a/out.txt", Size: 3}
	res := testutil.Ok(msgA)
	res.ArtifactsOut = []models.ResolvedArtifact{produced}
	runTask(t, orch, store, msgA, res)

	msgB := testutil.Drain(t, redis, 1)[0]
	if msgB.TaskDefinitionID != "b" {
		t.Fatalf("expected b dispatched after a, got %s", msgB.TaskDefinitionID)
	}
	if len(msgB.ArtifactsIn) != 1 || msgB.ArtifactsIn[0] != produced {
		t.Errorf("b ArtifactsIn = %+v, want [%+v]", msgB.ArtifactsIn, produced)
	}
}

func TestCompletedEventHasFinalTaskStates(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)

	startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:          uuid.NewString(),
		Name:        "Final States",
		Tasks:       []models.TaskDefinition{{ID: "only", Name: "Only", Type: "generic", Dependencies: []string{}}},
		MaxParallel: 10,
	})

	msg := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, store, msg, testutil.Ok(msg))

	var done *models.WorkflowExecution
	for _, ev := range rec.Events() {
		if ev.Type == models.WSEventWorkflowCompleted {
			done, _ = ev.Payload.(*models.WorkflowExecution)
		}
	}
	if done == nil {
		t.Fatalf("no %s event with a *WorkflowExecution payload", models.WSEventWorkflowCompleted)
	}
	if len(done.Tasks) != 1 {
		t.Fatalf("%s event lists %d tasks, want 1", models.WSEventWorkflowCompleted, len(done.Tasks))
	}
	if got := done.Tasks[0].Status; got != models.TaskStatusCompleted {
		t.Errorf("task status = %s in %s event, want %s", got, models.WSEventWorkflowCompleted, models.TaskStatusCompleted)
	}
}

func TestDuplicateResultIsDropped(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)

	startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Duplicate Result",
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
			{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"}},
		},
		MaxParallel: 10,
	})

	msgA := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, store, msgA, testutil.Ok(msgA))
	testutil.Drain(t, redis, 1)

	if err := orch.ProcessResult(context.Background(), testutil.Ok(msgA)); err != nil {
		t.Fatalf("redelivered result: ProcessResult = %v, want nil", err)
	}
	completed := 0
	for _, ev := range rec.Events() {
		if ev.Type == models.WSEventTaskCompleted {
			completed++
		}
	}
	if completed != 1 {
		t.Errorf("%d %s events after a redelivered result, want 1", completed, models.WSEventTaskCompleted)
	}
}

func TestResultWithoutAttemptIsApplied(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)
	ctx := context.Background()

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Legacy Result",
		Tasks: []models.TaskDefinition{{
			ID: "t", Name: "T", Type: "generic", Dependencies: []string{},
			RetryPolicy: &models.RetryPolicy{MaxRetries: 3, InitialDelay: time.Hour, MaxDelay: time.Hour, BackoffMultiple: 1},
		}},
		MaxParallel: 10,
	})

	msg := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, store, msg, testutil.Fail(msg, "transient"))
	// The retry poller would re-enqueue the task; the worker's pickup is all
	// this test needs to put attempt 1 in running.
	if err := orch.MarkTaskRunning(ctx, msg.TaskExecID, "testutil"); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}
	if row := testutil.TaskRow(t, store, exec.ID, "t"); row.Status != models.TaskStatusRunning || row.RetryCount != 1 {
		t.Fatalf("task is %s on attempt %d; want running on attempt 1", row.Status, row.RetryCount)
	}

	// A worker built before TaskResult.RetryCount existed publishes none.
	legacy := testutil.Ok(msg)
	legacy.RetryCount = nil
	if err := orch.ProcessResult(ctx, legacy); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := testutil.TaskRow(t, store, exec.ID, "t").Status; got != models.TaskStatusCompleted {
		t.Errorf("task status = %s after a result without retry_count, want %s", got, models.TaskStatusCompleted)
	}
}

// hubLike marshals each payload on its own goroutine, as api.Hub.Run does,
// so the race detector sees any write the orchestrator makes to a payload
// after broadcasting it. It keeps marshalling for a while, timed by the clock
// rather than a channel, so the reads overlap later writes without a
// happens-before edge that would hide them.
type hubLike struct {
	testutil.Recorder
	wg sync.WaitGroup
}

const hubMarshalFor = 300 * time.Millisecond

func (h *hubLike) Broadcast(ev models.WebSocketEvent) {
	h.Recorder.Broadcast(ev)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for start := time.Now(); time.Since(start) < hubMarshalFor; {
			// Only the reads matter here; the payloads are plain structs
			// that always marshal.
			_, _ = json.Marshal(ev)
		}
	}()
}

func TestExecutionEventsAreSnapshots(t *testing.T) {
	store, redis := testutil.Env(t)
	hub := &hubLike{}
	t.Cleanup(hub.wg.Wait)
	orch := orchestrator.NewOrchestrator(store, redis, hub)

	startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Snapshot Events",
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
			{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"}},
		},
		MaxParallel: 10,
	})

	// workflow.started is still being marshalled when these results arrive
	// and replace the cached task rows.
	msgA := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, store, msgA, testutil.Ok(msgA))
	msgB := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, store, msgB, testutil.Ok(msgB))
	testutil.Eventually(t, eventWait, func() bool { return hasEvent(&hub.Recorder, models.WSEventWorkflowCompleted) })
}

// The worker runs a task even when its pickup write fails (worker.go logs
// and carries on), so the result can find the row still queued.
func TestResultForQueuedRowIsApplied(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Missed Pickup",
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
			{ID: "c", Name: "C", Type: "generic", Dependencies: []string{}},
		},
		MaxParallel: 1,
	})

	first := testutil.Drain(t, redis, 1)[0]
	testutil.Eventually(t, eventWait, func() bool {
		return testutil.TaskRow(t, store, exec.ID, first.TaskDefinitionID).Status == models.TaskStatusQueued
	})
	if err := orch.ProcessResult(context.Background(), testutil.Ok(first)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if got := testutil.TaskRow(t, store, exec.ID, first.TaskDefinitionID).Status; got != models.TaskStatusCompleted {
		t.Errorf("task %s status = %s, want %s", first.TaskDefinitionID, got, models.TaskStatusCompleted)
	}
	// With one slot, the other task is dispatched only if the result released it.
	second := testutil.Drain(t, redis, 1)[0]
	if second.TaskDefinitionID == first.TaskDefinitionID {
		t.Errorf("dispatched %s again, want the other task", second.TaskDefinitionID)
	}
}
