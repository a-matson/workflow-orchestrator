//go:build integration

package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	runTask(t, orch, msgA, testutil.Ok(msgA))

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
	runTask(t, orch, msg, testutil.Fail(msg, "transient error"))

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
	runTask(t, orch, msg, testutil.Fail(msg, "fatal error"))

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
// orchestrator has accepted res.
func runTask(t *testing.T, orch *orchestrator.Orchestrator, msg *models.TaskMessage, res *models.TaskResult) {
	t.Helper()
	ctx := context.Background()
	if err := orch.MarkTaskRunning(ctx, msg.TaskExecID, "testutil", msg.RetryCount); err != nil {
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
	runTask(t, orch, msgA, res)

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
	runTask(t, orch, msg, testutil.Ok(msg))

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
	runTask(t, orch, msgA, testutil.Ok(msgA))
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
			RetryPolicy: &models.RetryPolicy{MaxRetries: 3, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond, BackoffMultiple: 1},
		}},
		MaxParallel: 10,
	})

	msg := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, msg, testutil.Fail(msg, "transient"))
	// Stands in for the retry poller's tick.
	orch.DispatchDue(ctx)
	msg = testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, msg.TaskExecID, "testutil", msg.RetryCount); err != nil {
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
	runTask(t, orch, msgA, testutil.Ok(msgA))
	msgB := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, msgB, testutil.Ok(msgB))
	testutil.Eventually(t, eventWait, func() bool { return hasEvent(&hub.Recorder, models.WSEventWorkflowCompleted) })
}

// The worker runs a task when its pickup write fails with anything but a
// conflict (worker.go logs and carries on), so the result can find the row
// still queued.
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
	// With MaxParallel 1, the other task is dispatched only once the result
	// has moved this row out of queued.
	second := testutil.Drain(t, redis, 1)[0]
	if second.TaskDefinitionID == first.TaskDefinitionID {
		t.Errorf("dispatched %s again, want the other task", second.TaskDefinitionID)
	}
}

func TestStaleAttemptIsDropped(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)
	ctx := context.Background()

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Stale Attempt",
		Tasks: []models.TaskDefinition{{
			ID: "t", Name: "T", Type: "generic", Dependencies: []string{},
			RetryPolicy: &models.RetryPolicy{MaxRetries: 3, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond, BackoffMultiple: 1},
		}},
		MaxParallel: 10,
	})

	attempt0 := testutil.Drain(t, redis, 1)[0]
	runTask(t, orch, attempt0, testutil.Fail(attempt0, "transient"))
	// Stands in for the retry poller's tick.
	orch.DispatchDue(ctx)
	attempt1 := testutil.Drain(t, redis, 1)[0]
	// A redelivered attempt-0 message arrives while attempt 1 is queued. Only
	// the attempt guard rejects it: the status alone would match.
	if err := orch.MarkTaskRunning(ctx, attempt0.TaskExecID, "testutil", attempt0.RetryCount); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("MarkTaskRunning for attempt 0 = %v, want ErrConflict", err)
	}
	if row := testutil.TaskRow(t, store, exec.ID, "t"); row.Status != models.TaskStatusQueued || row.RetryCount != 1 {
		t.Fatalf("task is %s on attempt %d after a stale pickup; want queued on attempt 1", row.Status, row.RetryCount)
	}
	if err := orch.MarkTaskRunning(ctx, attempt1.TaskExecID, "testutil", attempt1.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}

	// Attempt 0's failure arrives again while attempt 1 runs.
	if err := orch.ProcessResult(ctx, testutil.Fail(attempt0, "late duplicate")); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	row := testutil.TaskRow(t, store, exec.ID, "t")
	if row.Status != models.TaskStatusRunning || row.RetryCount != 1 {
		t.Errorf("task is %s on attempt %d after a stale attempt-0 result; want running on attempt 1", row.Status, row.RetryCount)
	}
}

// openTasks counts execID's tasks that hold a dispatch: queued or running.
func openTasks(t *testing.T, store *persistence.Store, execID string) int {
	t.Helper()
	tasks, err := store.ListTaskExecutions(context.Background(), execID)
	if err != nil {
		t.Fatalf("list task executions of %s: %v", execID, err)
	}
	n := 0
	for _, task := range tasks {
		if task.Status == models.TaskStatusQueued || task.Status == models.TaskStatusRunning {
			n++
		}
	}
	return n
}

func independentTasks(ids ...string) []models.TaskDefinition {
	tasks := make([]models.TaskDefinition, 0, len(ids))
	for _, id := range ids {
		tasks = append(tasks, models.TaskDefinition{ID: id, Name: id, Type: "generic", Dependencies: []string{}})
	}
	return tasks
}

// REL-12: a duplicate result must not let dispatch exceed MaxParallel.
func TestDuplicateResultDoesNotExceedMaxParallel(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)
	ctx := context.Background()
	const maxParallel = 2
	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Duplicate Slot", MaxParallel: maxParallel,
		Tasks: independentTasks("a", "b", "c", "d", "e"),
	})
	overLimit := func() bool { return openTasks(t, store, exec.ID) > maxParallel }

	msgs := testutil.Drain(t, redis, 2)
	m1, m2 := msgs[0], msgs[1]
	for _, m := range msgs {
		if err := orch.MarkTaskRunning(ctx, m.TaskExecID, "testutil", m.RetryCount); err != nil {
			t.Fatalf("MarkTaskRunning: %v", err)
		}
	}
	if err := orch.ProcessResult(ctx, testutil.Ok(m1)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	m3 := testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, m3.TaskExecID, "testutil", m3.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}
	if err := orch.ProcessResult(ctx, testutil.Ok(m1)); err != nil {
		t.Fatalf("duplicate result: ProcessResult = %v, want nil", err)
	}
	if err := orch.ProcessResult(ctx, testutil.Ok(m2)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	testutil.Never(t, 300*time.Millisecond, overLimit)
	// The capacity m2 freed must be reused; an orchestrator that stopped
	// dispatching altogether would pass the over-limit check above.
	testutil.Drain(t, redis, 1)
}

// REL-12: recovery must count the tasks already queued or running against
// MaxParallel instead of starting the execution with every slot free.
func TestRecoveryDoesNotExceedMaxParallel(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)
	const maxParallel = 2
	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Recovered Slots", MaxParallel: maxParallel,
		Tasks: independentTasks("a", "b", "c", "d", "e"),
	})
	testutil.Drain(t, redis, maxParallel)

	restarted := orchestrator.NewOrchestrator(store, redis, rec)
	if err := restarted.RecoverInFlightExecutions(context.Background()); err != nil {
		t.Fatalf("RecoverInFlightExecutions: %v", err)
	}
	testutil.Never(t, 300*time.Millisecond, func() bool { return openTasks(t, store, exec.ID) > maxParallel })
}

// REL-8: a restart kills every in-process worker, so a task left queued or
// running must be sent again, not left waiting for a message that no longer
// exists. A message that did survive in Redis only duplicates the re-sent
// one, and the pickup lets exactly one of them run.
func TestRecoveryRedeliversOpenTask(t *testing.T) {
	tests := []struct {
		name string
		// held drains the task's message before the restart; pickedUp also
		// marks it running on the worker the restart kills.
		held, pickedUp bool
		wantQueued     int
	}{
		{name: "running", held: true, pickedUp: true, wantQueued: 1},
		{name: "queued, message lost", held: true, wantQueued: 1},
		{name: "queued, message left in Redis", wantQueued: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orch, store, redis, rec := setupOrchestrator(t)
			ctx := context.Background()
			exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
				ID: uuid.NewString(), Name: "Redelivery", MaxParallel: 1,
				Tasks: independentTasks("a"),
			})
			if tt.held {
				m := testutil.Drain(t, redis, 1)[0]
				if tt.pickedUp {
					if err := orch.MarkTaskRunning(ctx, m.TaskExecID, "worker-dead", m.RetryCount); err != nil {
						t.Fatalf("MarkTaskRunning: %v", err)
					}
				}
			}

			restarted := orchestrator.NewOrchestrator(store, redis, rec)
			if err := restarted.RecoverInFlightExecutions(ctx); err != nil {
				t.Fatalf("RecoverInFlightExecutions: %v", err)
			}
			testutil.Eventually(t, eventWait, func() bool { return testutil.Queued(t, redis, exec.ID) == tt.wantQueued })

			var ran *models.TaskMessage
			for _, m := range testutil.Drain(t, redis, tt.wantQueued) {
				err := restarted.MarkTaskRunning(ctx, m.TaskExecID, "testutil", m.RetryCount)
				switch {
				case err == nil && ran == nil:
					ran = m
				case errors.Is(err, persistence.ErrConflict) && ran != nil:
				default:
					t.Fatalf("MarkTaskRunning = %v, want one success and then conflicts", err)
				}
			}
			if err := restarted.ProcessResult(ctx, testutil.Ok(ran)); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			testutil.Eventually(t, eventWait, func() bool { return execStatus(t, store, exec.ID) == models.WorkflowStatusCompleted })
		})
	}
}

// REL-10: recovery must find every open execution, not only those among the
// newest 200 of all executions.
func TestRecovery_Beyond200(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)
	ctx := context.Background()
	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Buried", MaxParallel: 1,
		Tasks: independentTasks("a"),
	})
	testutil.Drain(t, redis, 1)
	if _, err := store.Pool().Exec(ctx, `
		INSERT INTO workflow_executions (id, workflow_id, workflow_name, status, created_at)
		SELECT gen_random_uuid()::text, workflow_id, workflow_name, 'completed', created_at + n * interval '1 second'
		FROM workflow_executions, generate_series(1, 200) AS n WHERE id = $1
	`, exec.ID); err != nil {
		t.Fatalf("insert newer executions: %v", err)
	}

	restarted := orchestrator.NewOrchestrator(store, redis, rec)
	if err := restarted.RecoverInFlightExecutions(ctx); err != nil {
		t.Fatalf("RecoverInFlightExecutions: %v", err)
	}
	testutil.Eventually(t, eventWait, func() bool { return testutil.Queued(t, redis, exec.ID) == 1 })
}

// REL-16: recovery must resume an execution with the definition it started
// with, not the stored one edited since.
func TestRecovery_UsesSnapshot(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)
	ctx := context.Background()
	def := &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Snapshot", MaxParallel: 1,
		Tasks: independentTasks("a", "b"),
	}
	exec := startWorkflow(t, orch, store, def)
	testutil.Drain(t, redis, 1)
	edited := *def
	edited.Tasks = independentTasks("a")
	testutil.SaveDef(t, store, &edited)

	restarted := orchestrator.NewOrchestrator(store, redis, rec)
	if err := restarted.RecoverInFlightExecutions(ctx); err != nil {
		t.Fatalf("RecoverInFlightExecutions: %v", err)
	}
	first := testutil.Drain(t, redis, 1)[0]
	runTask(t, restarted, first, testutil.Ok(first))
	second := testutil.Drain(t, redis, 1)[0]
	runTask(t, restarted, second, testutil.Ok(second))

	testutil.Eventually(t, eventWait, func() bool { return execStatus(t, store, exec.ID) == models.WorkflowStatusCompleted })
	for _, id := range []string{"a", "b"} {
		if got := testutil.TaskRow(t, store, exec.ID, id).Status; got != models.TaskStatusCompleted {
			t.Errorf("task %s = %s, want completed", id, got)
		}
	}
}

// REL-9: a result for an execution this process has not loaded, as when
// recovery skipped it (listing cap, failed recoverExecution), must be applied and
// advance the DAG, not leave its row running forever.
func TestResultForUnloadedExecutionIsApplied(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)
	ctx := context.Background()
	exec := startWorkflow(t, orch, store, chainDef())
	msgA := testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, msgA.TaskExecID, "testutil", msgA.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}

	unloaded := orchestrator.NewOrchestrator(store, redis, rec)
	if err := unloaded.ProcessResult(ctx, testutil.Ok(msgA)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := testutil.TaskRow(t, store, exec.ID, "a").Status; got != models.TaskStatusCompleted {
		t.Fatalf("task a = %s, want %s", got, models.TaskStatusCompleted)
	}
	testutil.Eventually(t, eventWait, func() bool { return testutil.Queued(t, redis, exec.ID) == 1 })
}

// A cancel that commits after a result's miss-load, while the execution is
// not yet registered, must not leave the loaded context active for good.
func TestExecutionCancelledDuringLoadIsNotLeftActive(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)
	ctx := context.Background()
	exec := startWorkflow(t, orch, store, chainDef())
	msgA := testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, msgA.TaskExecID, "testutil", msgA.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}

	unloaded := orchestrator.NewOrchestrator(store, redis, rec)
	var cancelErr error
	t.Cleanup(orchestrator.SetBeforeRegister(func() { _, cancelErr = unloaded.CancelExecution(ctx, exec.ID) }))
	if err := unloaded.ProcessResult(ctx, testutil.Ok(msgA)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if cancelErr != nil {
		t.Fatalf("CancelExecution: %v", cancelErr)
	}
	if n := unloaded.GetMetrics()["active_workflows"]; n != 0 {
		t.Errorf("active workflows = %d, want 0", n)
	}
	if got := testutil.TaskRow(t, store, exec.ID, "a").Status; got != models.TaskStatusCancelled {
		t.Errorf("task a = %s, want %s", got, models.TaskStatusCancelled)
	}
}

// recoverAfterCrash starts a one-task workflow whose task was running on a
// worker that the restart killed, recovers it on a new orchestrator, and
// returns that orchestrator with the killed worker's message and the re-sent one.
func recoverAfterCrash(t *testing.T) (restarted *orchestrator.Orchestrator, store *persistence.Store, execID string, dead, resent *models.TaskMessage) {
	t.Helper()
	orch, store, redis, rec := setupOrchestrator(t)
	ctx := context.Background()
	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Crash", MaxParallel: 1,
		Tasks: independentTasks("a"),
	})
	dead = testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, dead.TaskExecID, "worker-dead", dead.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}
	restarted = orchestrator.NewOrchestrator(store, redis, rec)
	if err := restarted.RecoverInFlightExecutions(ctx); err != nil {
		t.Fatalf("RecoverInFlightExecutions: %v", err)
	}
	return restarted, store, exec.ID, dead, testutil.Drain(t, redis, 1)[0]
}

// deadWorkerResult is the result the killed worker published just before
// the crash, still waiting in the result queue.
func deadWorkerResult(m *models.TaskMessage) *models.TaskResult {
	r := testutil.Ok(m)
	r.WorkerID = "worker-dead"
	return r
}

func execStatus(t *testing.T, store *persistence.Store, execID string) models.WorkflowStatus {
	t.Helper()
	got, err := store.GetWorkflowExecution(context.Background(), execID)
	if err != nil {
		t.Fatalf("GetWorkflowExecution: %v", err)
	}
	return got.Status
}

// A result from before the restart must not close the attempt that a new
// worker is running since: dependents would start on the new run's
// half-written artifacts, and a failure would start a retry beside it.
func TestPreCrashResultDoesNotCloseRerunningAttempt(t *testing.T) {
	orch, store, execID, dead, resent := recoverAfterCrash(t)
	ctx := context.Background()
	if err := orch.MarkTaskRunning(ctx, resent.TaskExecID, "testutil", resent.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning after recovery: %v", err)
	}

	if err := orch.ProcessResult(ctx, deadWorkerResult(dead)); err != nil {
		t.Fatalf("pre-crash result: ProcessResult = %v, want nil", err)
	}
	if row := testutil.TaskRow(t, store, execID, "a"); row.Status != models.TaskStatusRunning {
		t.Fatalf("task after pre-crash result = %s, want running", row.Status)
	}

	if err := orch.ProcessResult(ctx, testutil.Ok(resent)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	testutil.Eventually(t, eventWait, func() bool { return execStatus(t, store, execID) == models.WorkflowStatusCompleted })
}

// A result from before the restart that arrives while the re-sent attempt is
// still queued completes the task, and the re-sent message is then dropped.
func TestPreCrashResultCompletesQueuedAttempt(t *testing.T) {
	orch, store, execID, dead, resent := recoverAfterCrash(t)
	ctx := context.Background()

	if err := orch.ProcessResult(ctx, deadWorkerResult(dead)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	testutil.Eventually(t, eventWait, func() bool { return execStatus(t, store, execID) == models.WorkflowStatusCompleted })
	if err := orch.MarkTaskRunning(ctx, resent.TaskExecID, "testutil", resent.RetryCount); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("pickup of the re-sent message = %v, want ErrConflict", err)
	}
}

// One task that recovery cannot reset must not strand the rest of its
// execution: the others are re-sent and the execution stays active.
func TestRecoveryContinuesPastTaskStoreError(t *testing.T) {
	orch, store, redis, rec := setupOrchestrator(t)
	ctx := context.Background()
	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Partial Recovery", MaxParallel: 2,
		Tasks: independentTasks("a", "b"),
	})
	for _, m := range testutil.Drain(t, redis, 2) {
		if err := orch.MarkTaskRunning(ctx, m.TaskExecID, "worker-dead", m.RetryCount); err != nil {
			t.Fatalf("MarkTaskRunning: %v", err)
		}
	}
	broken := testutil.TaskRow(t, store, exec.ID, "a")
	// A trigger is the cheapest way to fail one store write on a real database.
	if _, err := store.Pool().Exec(ctx, `
		CREATE FUNCTION fail_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected store error'; END $$;
		CREATE TRIGGER fail_reset BEFORE UPDATE ON task_executions FOR EACH ROW
		WHEN (NEW.status = 'pending' AND OLD.id::text = '`+broken.ID+`') EXECUTE FUNCTION fail_write();`); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}

	restarted := orchestrator.NewOrchestrator(store, redis, rec)
	if err := restarted.RecoverInFlightExecutions(ctx); err != nil {
		t.Fatalf("RecoverInFlightExecutions: %v", err)
	}

	testutil.Eventually(t, eventWait, func() bool { return testutil.Queued(t, redis, exec.ID) == 1 })
	if m := testutil.Drain(t, redis, 1)[0]; m.TaskDefinitionID != "b" {
		t.Errorf("re-sent task = %s, want b", m.TaskDefinitionID)
	}
	if n := restarted.GetMetrics()["active_workflows"]; n != 1 {
		t.Errorf("active workflows = %d, want 1", n)
	}
	if row := testutil.TaskRow(t, store, exec.ID, "a"); row.Status != models.TaskStatusRunning {
		t.Errorf("task a = %s, want running (its reset failed)", row.Status)
	}
}

// A failed execution leaves the active map, so a sibling still running or
// waiting for its retry would otherwise stay open forever (REL-11).
func TestFailedWorkflow_CancelsOpenSiblings(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)
	ctx := context.Background()

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID:   uuid.NewString(),
		Name: "Fail Siblings",
		Tasks: []models.TaskDefinition{
			{ID: "doomed", Name: "Doomed", Type: "generic", Dependencies: []string{},
				RetryPolicy: &models.RetryPolicy{MaxRetries: 0}},
			{ID: "sibling", Name: "Sibling", Type: "generic", Dependencies: []string{}},
		},
		MaxParallel: 10,
	})

	msgs := testutil.Drain(t, redis, 2)
	byDef := map[string]*models.TaskMessage{}
	for _, m := range msgs {
		byDef[m.TaskDefinitionID] = m
	}
	if err := orch.MarkTaskRunning(ctx, byDef["sibling"].TaskExecID, "testutil", 0); err != nil {
		t.Fatalf("MarkTaskRunning sibling: %v", err)
	}
	stopped := &recordingCanceller{}
	orch.SetTaskCanceller(stopped)
	runTask(t, orch, byDef["doomed"], testutil.Fail(byDef["doomed"], "fatal"))

	if got := testutil.TaskRow(t, store, exec.ID, "doomed").Status; got != models.TaskStatusDeadLetter {
		t.Errorf("doomed status = %s, want %s", got, models.TaskStatusDeadLetter)
	}
	tasks, err := store.ListTaskExecutions(ctx, exec.ID)
	if err != nil {
		t.Fatalf("list task executions: %v", err)
	}
	// Whatever a cancel may close is still open; none of it may outlive the failure.
	open := models.TaskFrom(models.TaskStatusCancelled)
	for _, task := range tasks {
		if slices.Contains(open, task.Status) {
			t.Errorf("task %s left %s under the failed execution", task.TaskDefinitionID, task.Status)
		}
	}
	if got := testutil.TaskRow(t, store, exec.ID, "sibling").Status; got != models.TaskStatusCancelled {
		t.Errorf("sibling status = %s, want %s", got, models.TaskStatusCancelled)
	}
	if got := stopped.ids(); !slices.Contains(got, byDef["sibling"].TaskExecID) {
		t.Errorf("stopped runs = %v, want the sibling's %s", got, byDef["sibling"].TaskExecID)
	}
	stored, err := store.GetWorkflowExecution(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if stored.Status != models.WorkflowStatusFailed {
		t.Errorf("execution status = %s, want %s", stored.Status, models.WorkflowStatusFailed)
	}
}

// A cancel whose task close fails must not commit the execution's terminal
// status alone: recovery never revisits a terminal execution, so its queued
// rows would still be picked up and run.
func TestCancelExecution_AllOrNothing(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)
	ctx := context.Background()

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Atomic Cancel", MaxParallel: 1,
		Tasks: independentTasks("a"),
	})
	msg := testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, msg.TaskExecID, "testutil", msg.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}
	// A trigger is the cheapest way to fail one store write on a real database.
	if _, err := store.Pool().Exec(ctx, `
		CREATE FUNCTION fail_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected store error'; END $$;
		CREATE TRIGGER fail_cancel BEFORE UPDATE ON task_executions FOR EACH ROW
		WHEN (NEW.status = 'cancelled') EXECUTE FUNCTION fail_write();`); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}

	if _, err := orch.CancelExecution(ctx, exec.ID); err == nil {
		t.Error("CancelExecution succeeded although its task close failed")
	}
	stored, err := store.GetWorkflowExecution(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if stored.Status != models.WorkflowStatusRunning {
		t.Errorf("execution status = %s, want %s", stored.Status, models.WorkflowStatusRunning)
	}
}

type recordingCanceller struct {
	mu      sync.Mutex
	stopped []string
}

func (c *recordingCanceller) Cancel(taskExecID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = append(c.stopped, taskExecID)
}

func (c *recordingCanceller) ids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.stopped)
}

// A cancel must stop the running task's container, and the failure its
// killed run may still report must not reopen the cancelled row.
func TestCancelExecution_StopsRunningTask(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)
	ctx := context.Background()
	stopped := &recordingCanceller{}
	orch.SetTaskCanceller(stopped)

	exec := startWorkflow(t, orch, store, &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Cancel Running", MaxParallel: 1,
		Tasks: independentTasks("a"),
	})
	msg := testutil.Drain(t, redis, 1)[0]
	if err := orch.MarkTaskRunning(ctx, msg.TaskExecID, "testutil", msg.RetryCount); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}

	if _, err := orch.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatalf("CancelExecution: %v", err)
	}
	if got := stopped.ids(); !slices.Equal(got, []string{msg.TaskExecID}) {
		t.Errorf("stopped runs = %v, want [%s]", got, msg.TaskExecID)
	}

	if err := orch.ProcessResult(ctx, testutil.Fail(msg, "context canceled")); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := testutil.TaskRow(t, store, exec.ID, "a").Status; got != models.TaskStatusCancelled {
		t.Errorf("task status after its late failure = %s, want %s", got, models.TaskStatusCancelled)
	}
	if n := orch.GetMetrics()["active_workflows"]; n != 0 {
		t.Errorf("active workflows after a late result for a cancelled execution = %d, want 0", n)
	}
}
