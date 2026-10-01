//go:build integration

package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/scheduler"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func runsOf(t *testing.T, store *persistence.Store, workflowID string) int {
	t.Helper()
	var n int
	if err := store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_executions WHERE workflow_id = $1`, workflowID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// F1: a due schedule starts exactly one run, even when two ticks race, and
// moves its next_run_at past now; a missed schedule fires once, not per miss.
func TestCronScheduler_StartsOneRunPerFiring(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	cron := scheduler.NewCronScheduler(store, orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{}))
	now := time.Now().UTC()
	due := now.Add(-3 * time.Hour) // missed three hourly firings while "down"
	def := &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Hourly", MaxParallel: 1, Schedule: "@hourly", NextRunAt: &due,
		Tasks: []models.TaskDefinition{{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}}},
	}
	testutil.SaveDef(t, store, def)

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { cron.Tick(ctx, now) })
	}
	wg.Wait()
	if n := runsOf(t, store, def.ID); n != 1 {
		t.Fatalf("runs after two racing ticks = %d, want 1", n)
	}
	got, err := store.GetWorkflowDefinition(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.After(now) || got.NextRunAt.Sub(now) > time.Hour {
		t.Errorf("next_run_at = %v, want within the next hour after %s", got.NextRunAt, now)
	}

	cron.Tick(ctx, now)
	if n := runsOf(t, store, def.ID); n != 1 {
		t.Errorf("runs after a tick with nothing due = %d, want 1", n)
	}
	testutil.Drain(t, redis, 1)
}
