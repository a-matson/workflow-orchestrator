//go:build integration

package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// Postgres keeps microseconds, so fixtures are truncated to compare with Equal.
func pgNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// seedTask stores a task in status with retryCount and every patchable column
// non-NULL and distinct, so a test can tell "kept" from "cleared" or "rewritten".
func seedTask(t *testing.T, store *persistence.Store, status models.TaskStatus, retryCount int) *models.TaskExecution {
	t.Helper()
	ctx := context.Background()
	exec := createExecution(t, store, makeWorkflowDef("transitions"), models.WorkflowStatusRunning)
	now := pgNow()
	task := &models.TaskExecution{
		ID:               uuid.NewString(),
		WorkflowExecID:   exec.ID,
		TaskDefinitionID: "t1",
		TaskName:         "Task 1",
		TaskType:         "generic",
		Status:           status,
		RetryCount:       retryCount,
		MaxRetries:       3,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := store.CreateTaskExecution(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	queued := now.Add(-4 * time.Minute)
	started := now.Add(-3 * time.Minute)
	completed := now.Add(-2 * time.Minute)
	nextRetry := now.Add(-time.Minute)
	task.WorkerID = "w0"
	task.QueuedAt = &queued
	task.StartedAt = &started
	task.CompletedAt = &completed
	task.NextRetryAt = &nextRetry
	task.Error = "old error"
	task.Output = json.RawMessage(`{"old":true}`)
	task.Logs = []models.LogEntry{{Timestamp: queued, Level: "info", Message: "old"}}
	task.ArtifactsOut = []models.ResolvedArtifact{{Path: "old.txt", MinioKey: "k/old.txt", Size: 1}}
	if err := store.UpdateTaskExecution(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return getTask(t, store, task.ID)
}

func getTask(t *testing.T, store *persistence.Store, id string) *models.TaskExecution {
	t.Helper()
	task, err := store.GetTaskExecution(context.Background(), id)
	if err != nil {
		t.Fatalf("get task %s: %v", id, err)
	}
	return task
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func messages(logs []models.LogEntry) []string {
	out := make([]string, len(logs))
	for i, l := range logs {
		out[i] = l.Message
	}
	return out
}

func TestTransitionTask_Allowed(t *testing.T) {
	store := setupStore(t)
	for _, to := range []models.TaskStatus{
		models.TaskStatusQueued, models.TaskStatusRunning, models.TaskStatusCompleted,
		models.TaskStatusRetrying, models.TaskStatusDeadLetter, models.TaskStatusPending,
		models.TaskStatusCancelled,
	} {
		for _, from := range models.TaskFrom(to) {
			t.Run(fmt.Sprintf("%s->%s", from, to), func(t *testing.T) {
				before := seedTask(t, store, from, 0)
				started := pgNow()
				retries := 1
				got, err := store.TransitionTask(context.Background(), before.ID, 0, to, persistence.TaskPatch{
					WorkerID:   "w1",
					StartedAt:  &started,
					RetryCount: &retries,
					Output:     json.RawMessage(`{"new":true}`),
					Logs:       []models.LogEntry{{Timestamp: started, Level: "info", Message: "new"}},
				})
				if err != nil {
					t.Fatalf("TransitionTask: %v", err)
				}
				if got.Status != to {
					t.Errorf("status = %s, want %s", got.Status, to)
				}
				if got.WorkerID != "w1" || got.StartedAt == nil || !got.StartedAt.Equal(started) ||
					got.RetryCount != 1 || string(got.Output) != `{"new": true}` {
					t.Errorf("patched columns not written: worker=%q started=%v retry=%d output=%s",
						got.WorkerID, got.StartedAt, got.RetryCount, got.Output)
				}
				if want := []string{"old", "new"}; !slices.Equal(messages(got.Logs), want) {
					t.Errorf("logs = %v, want %v", messages(got.Logs), want)
				}
				if !equalTime(got.QueuedAt, before.QueuedAt) || !equalTime(got.CompletedAt, before.CompletedAt) ||
					!equalTime(got.NextRetryAt, before.NextRetryAt) ||
					got.Error != before.Error || !slices.Equal(got.ArtifactsOut, before.ArtifactsOut) {
					t.Errorf("unpatched columns changed: queued=%v completed=%v next_retry=%v error=%q artifacts=%v",
						got.QueuedAt, got.CompletedAt, got.NextRetryAt, got.Error, got.ArtifactsOut)
				}
				if stored := getTask(t, store, before.ID); stored.Status != to {
					t.Errorf("stored status = %s, want %s", stored.Status, to)
				}
			})
		}
	}
}

// Complements Allowed, which leaves these columns nil.
func TestTransitionTask_PatchesRemainingColumns(t *testing.T) {
	store := setupStore(t)
	before := seedTask(t, store, models.TaskStatusRunning, 0)
	now := pgNow()
	next := now.Add(time.Minute)
	errMsg := "boom"
	artifacts := []models.ResolvedArtifact{{Path: "new.txt", MinioKey: "k/new.txt", Size: 2}}
	got, err := store.TransitionTask(context.Background(), before.ID, 0, models.TaskStatusRetrying, persistence.TaskPatch{
		QueuedAt:     &now,
		CompletedAt:  &now,
		NextRetryAt:  &next,
		Error:        &errMsg,
		ArtifactsOut: artifacts,
	})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if !got.QueuedAt.Equal(now) || got.CompletedAt == nil || !got.CompletedAt.Equal(now) ||
		got.NextRetryAt == nil || !got.NextRetryAt.Equal(next) || got.Error != errMsg ||
		!slices.Equal(got.ArtifactsOut, artifacts) {
		t.Errorf("patched columns not written: queued=%v completed=%v next_retry=%v error=%q artifacts=%v",
			got.QueuedAt, got.CompletedAt, got.NextRetryAt, got.Error, got.ArtifactsOut)
	}
	if got.WorkerID != before.WorkerID || got.RetryCount != before.RetryCount ||
		!equalTime(got.StartedAt, before.StartedAt) ||
		string(got.Output) != string(before.Output) || !slices.Equal(messages(got.Logs), messages(before.Logs)) {
		t.Errorf("unpatched columns changed: worker=%q retry=%d started=%v output=%s logs=%v",
			got.WorkerID, got.RetryCount, got.StartedAt, got.Output, messages(got.Logs))
	}
}

// A non-nil but empty RawMessage is still "unchanged", not invalid jsonb.
func TestTransitionTask_EmptyOutputKeepsOutput(t *testing.T) {
	store := setupStore(t)
	before := seedTask(t, store, models.TaskStatusRunning, 0)
	got, err := store.TransitionTask(context.Background(), before.ID, 0, models.TaskStatusCompleted,
		persistence.TaskPatch{Output: json.RawMessage{}})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if string(got.Output) != string(before.Output) {
		t.Errorf("output = %s, want %s", got.Output, before.Output)
	}
}

func TestTransitionTask_Conflict(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	before := seedTask(t, store, models.TaskStatusCompleted, 0)

	_, err := store.TransitionTask(ctx, before.ID, 0, models.TaskStatusRunning, persistence.TaskPatch{
		WorkerID: "w1",
		Logs:     []models.LogEntry{{Message: "new"}},
	})
	if !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("wrong from-status: error = %v, want ErrConflict", err)
	}
	after := getTask(t, store, before.ID)
	if after.Status != before.Status || after.WorkerID != before.WorkerID ||
		!after.UpdatedAt.Equal(before.UpdatedAt) || len(after.Logs) != len(before.Logs) {
		t.Errorf("row changed on conflict: before=%+v after=%+v", before, after)
	}

	if _, err := store.TransitionTask(ctx, uuid.NewString(), -1, models.TaskStatusCancelled, persistence.TaskPatch{}); !errors.Is(err, persistence.ErrConflict) {
		t.Errorf("missing id: error = %v, want ErrConflict", err)
	}
}

func TestTransitionTask_AttemptMismatch(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	task := seedTask(t, store, models.TaskStatusQueued, 1)

	if _, err := store.TransitionTask(ctx, task.ID, 0, models.TaskStatusRunning, persistence.TaskPatch{}); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("attempt 0 on retry_count 1: error = %v, want ErrConflict", err)
	}
	got, err := store.TransitionTask(ctx, task.ID, -1, models.TaskStatusRunning, persistence.TaskPatch{})
	if err != nil {
		t.Fatalf("attempt -1: %v", err)
	}
	if got.Status != models.TaskStatusRunning || got.RetryCount != 1 {
		t.Errorf("got status=%s retry=%d, want running/1", got.Status, got.RetryCount)
	}
}

func TestTransitionTask_ConcurrentSingleWinner(t *testing.T) {
	store := setupStore(t)
	task := seedTask(t, store, models.TaskStatusQueued, 0)

	const racers = 20
	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			_, errs[i] = store.TransitionTask(context.Background(), task.ID, 0, models.TaskStatusRunning,
				persistence.TaskPatch{WorkerID: fmt.Sprintf("w%d", i)})
		})
	}
	close(start)
	wg.Wait()

	wins, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, persistence.ErrConflict):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 || conflicts != racers-1 {
		t.Fatalf("wins=%d conflicts=%d, want 1 and %d", wins, conflicts, racers-1)
	}
}

func TestTransitionTask_NullLogsGuard(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	task := seedTask(t, store, models.TaskStatusRunning, 0)
	// UpdateTaskExecution marshals nil Logs as JSON null into the column.
	task.Logs = nil
	if err := store.UpdateTaskExecution(ctx, task); err != nil {
		t.Fatalf("store null logs: %v", err)
	}
	if logs := getTask(t, store, task.ID).Logs; logs != nil {
		t.Fatalf("precondition: logs = %v, want JSON null", logs)
	}

	got, err := store.TransitionTask(ctx, task.ID, 0, models.TaskStatusCompleted, persistence.TaskPatch{
		Logs: []models.LogEntry{{Message: "new"}},
	})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if want := []string{"new"}; !slices.Equal(messages(got.Logs), want) {
		t.Errorf("logs = %v, want %v", messages(got.Logs), want)
	}
}

func TestTransitionExecution_Allowed(t *testing.T) {
	store := setupStore(t)
	for _, to := range []models.WorkflowStatus{
		models.WorkflowStatusRunning, models.WorkflowStatusCompleted,
		models.WorkflowStatusFailed, models.WorkflowStatusCancelled,
	} {
		for _, from := range models.ExecFrom(to) {
			t.Run(fmt.Sprintf("%s->%s", from, to), func(t *testing.T) {
				exec := createExecution(t, store, makeWorkflowDef("transitions"), from)
				got, err := store.TransitionExecution(context.Background(), exec.ID, to, "why")
				if err != nil {
					t.Fatalf("TransitionExecution: %v", err)
				}
				if got.Status != to || got.Error != "why" {
					t.Errorf("got status=%s error=%q, want %s/why", got.Status, got.Error, to)
				}
				stored, err := store.GetWorkflowExecution(context.Background(), exec.ID)
				if err != nil {
					t.Fatalf("get execution: %v", err)
				}
				if stored.Status != to {
					t.Errorf("stored status = %s, want %s", stored.Status, to)
				}
			})
		}
	}
}

func TestTransitionExecution_Conflict(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	exec := createExecution(t, store, makeWorkflowDef("transitions"), models.WorkflowStatusCompleted)

	if _, err := store.TransitionExecution(ctx, exec.ID, models.WorkflowStatusCancelled, "late"); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("wrong from-status: error = %v, want ErrConflict", err)
	}
	after, err := store.GetWorkflowExecution(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if after.Status != models.WorkflowStatusCompleted || after.Error != "" || after.CompletedAt != nil {
		t.Errorf("row changed on conflict: %+v", after)
	}

	if _, err := store.TransitionExecution(ctx, uuid.NewString(), models.WorkflowStatusCancelled, ""); !errors.Is(err, persistence.ErrConflict) {
		t.Errorf("missing id: error = %v, want ErrConflict", err)
	}
}

func TestTransitionExecution_TerminalSetsCompletedAt(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	exec := createExecution(t, store, makeWorkflowDef("transitions"), models.WorkflowStatusPending)

	running, err := store.TransitionExecution(ctx, exec.ID, models.WorkflowStatusRunning, "")
	if err != nil {
		t.Fatalf("to running: %v", err)
	}
	if running.CompletedAt != nil || running.StartedAt == nil || running.Error != "" {
		t.Errorf("running: completed_at=%v started_at=%v error=%q, want nil/set/empty",
			running.CompletedAt, running.StartedAt, running.Error)
	}

	done, err := store.TransitionExecution(ctx, exec.ID, models.WorkflowStatusCompleted, "")
	if err != nil {
		t.Fatalf("to completed: %v", err)
	}
	if done.CompletedAt == nil || done.CompletedAt.Before(*done.StartedAt) {
		t.Errorf("completed: completed_at=%v, want set and not before started_at %v", done.CompletedAt, done.StartedAt)
	}
	if !done.StartedAt.Equal(*running.StartedAt) {
		t.Errorf("started_at moved from %v to %v", running.StartedAt, done.StartedAt)
	}
}

func TestTransitionExecution_EmptyErrMsgKeepsError(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	exec := createExecution(t, store, makeWorkflowDef("transitions"), models.WorkflowStatusPending)
	if _, err := store.TransitionExecution(ctx, exec.ID, models.WorkflowStatusRunning, "first"); err != nil {
		t.Fatalf("to running: %v", err)
	}
	got, err := store.TransitionExecution(ctx, exec.ID, models.WorkflowStatusFailed, "")
	if err != nil {
		t.Fatalf("to failed: %v", err)
	}
	if got.Error != "first" {
		t.Errorf("error = %q, want %q", got.Error, "first")
	}
}

func TestCancelOpenTasks(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	exec := createExecution(t, store, makeWorkflowDef("cancel"), models.WorkflowStatusCancelled)
	other := createExecution(t, store, makeWorkflowDef("other"), models.WorkflowStatusRunning)

	statuses := []models.TaskStatus{
		models.TaskStatusPending, models.TaskStatusQueued, models.TaskStatusRunning,
		models.TaskStatusRetrying, models.TaskStatusCompleted, models.TaskStatusDeadLetter,
	}
	create := func(execID, defID string, status models.TaskStatus) {
		t.Helper()
		now := pgNow()
		if err := store.CreateTaskExecution(ctx, &models.TaskExecution{
			ID: uuid.NewString(), WorkflowExecID: execID, TaskDefinitionID: defID, TaskName: defID,
			TaskType: "generic", Status: status, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create task %s: %v", defID, err)
		}
	}
	for _, st := range statuses {
		create(exec.ID, string(st), st)
	}
	create(other.ID, "other", models.TaskStatusRunning)

	got, err := store.CancelOpenTasks(ctx, exec.ID)
	if err != nil {
		t.Fatalf("CancelOpenTasks: %v", err)
	}
	var returned []string
	for _, task := range got {
		returned = append(returned, task.TaskDefinitionID)
		if task.Status != models.TaskStatusCancelled || task.CompletedAt == nil {
			t.Errorf("returned %s: status=%s completed_at=%v, want cancelled and stamped", task.TaskDefinitionID, task.Status, task.CompletedAt)
		}
	}
	slices.Sort(returned)
	if want := []string{"pending", "queued", "retrying", "running"}; !slices.Equal(returned, want) {
		t.Errorf("returned tasks = %v, want %v", returned, want)
	}

	stored, err := store.ListTaskExecutions(ctx, exec.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range stored {
		want := models.TaskStatusCancelled
		if !slices.Contains(models.TaskFrom(models.TaskStatusCancelled), models.TaskStatus(task.TaskDefinitionID)) {
			want = models.TaskStatus(task.TaskDefinitionID)
		}
		if task.Status != want {
			t.Errorf("stored %s = %s, want %s", task.TaskDefinitionID, task.Status, want)
		}
	}
	otherTasks, err := store.ListTaskExecutions(ctx, other.ID)
	if err != nil {
		t.Fatalf("list other tasks: %v", err)
	}
	if otherTasks[0].Status != models.TaskStatusRunning {
		t.Errorf("another execution's task = %s, want running", otherTasks[0].Status)
	}
}
