//go:build integration

package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/scheduler"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// F10: retention deletes finished runs older than the cutoff with their tasks
// and logs, and keeps recent and still-open runs.
func TestRetentionSweeper_DeletesOldFinishedRuns(t *testing.T) {
	store, _ := testutil.Env(t)
	ctx := context.Background()
	def := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "Retained",
		Tasks: []models.TaskDefinition{{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}}}}
	testutil.SaveDef(t, store, def)
	now := time.Now()
	run := func(status models.WorkflowStatus, completedAgo time.Duration) string {
		id := uuid.NewString()
		if _, err := store.Pool().Exec(ctx, `
			INSERT INTO workflow_executions (id, workflow_id, workflow_name, status, completed_at) VALUES ($1, $2, 'r', $3, $4);
		`, id, def.ID, status, now.Add(-completedAgo)); err != nil {
			t.Fatal(err)
		}
		task := &models.TaskExecution{ID: uuid.NewString(), WorkflowExecID: id, TaskDefinitionID: "a",
			TaskName: "A", TaskType: "generic", Status: models.TaskStatusCompleted, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateTaskExecution(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendTaskLogs(ctx, task.ID, []models.LogEntry{{Timestamp: now, Level: "info", Message: "x"}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := run(models.WorkflowStatusCompleted, 40*24*time.Hour)
	recent := run(models.WorkflowStatusFailed, 2*24*time.Hour)
	open := run(models.WorkflowStatusRunning, 40*24*time.Hour)

	scheduler.NewRetentionSweeper(store, 30*24*time.Hour, 0).Sweep(ctx, now)

	exists := func(id string) bool {
		var n int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM workflow_executions WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if exists(old) || !exists(recent) || !exists(open) {
		t.Errorf("after sweep: old %v recent %v open %v; want false true true", exists(old), exists(recent), exists(open))
	}
	var orphans int
	if err := store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM task_logs l LEFT JOIN task_executions t ON t.id = l.task_exec_id WHERE t.id IS NULL
	`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d log lines left without their task", orphans)
	}
}
