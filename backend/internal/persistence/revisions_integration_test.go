//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/migrations"
)

// Concurrent saves of one workflow must each get their own revision, not
// fail on the (workflow_id, revision) key.
func TestRevisions_ConcurrentSavesGetDistinctRevisions(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	id := uuid.NewString()
	const saves = 8

	var wg sync.WaitGroup
	errs := make([]error, saves)
	for i := range saves {
		wg.Add(1)
		go func() {
			defer wg.Done()
			now := time.Now()
			errs[i] = store.SaveWorkflowDefinition(ctx, &models.WorkflowDefinition{
				ID: id, Name: "Concurrent", MaxParallel: 1, CreatedAt: now, UpdatedAt: now,
				Tasks: []models.TaskDefinition{{ID: "a", Name: "a", Type: "generic", Dependencies: []string{}}},
			})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	revs, err := store.ListWorkflowRevisions(ctx, id, 50, 0)
	if err != nil {
		t.Fatalf("ListWorkflowRevisions: %v", err)
	}
	if len(revs) != saves || revs[0].Revision != saves || revs[saves-1].Revision != 1 {
		t.Fatalf("revisions = %+v, want %d down to 1", revs, saves)
	}
}

// 014 backfills revision 1 from the definition columns; it must decode to the
// same definition the store reads, and running it again must change nothing.
func TestRevisions_MigrationBackfill(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()
	def := &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Legacy", Description: "before revisions", Version: "2.0.0", MaxParallel: 3,
		Tags:        map[string]string{"team": "data"},
		GlobalRetry: &models.RetryPolicy{MaxRetries: 2, InitialDelay: time.Second},
		Schedule:    "@hourly",
		Alerts:      &models.WorkflowAlerts{OnFailure: &models.AlertTarget{URL: "https://hooks.example.com/x"}},
		Tasks:       []models.TaskDefinition{{ID: "a", Name: "a", Type: "generic", Dependencies: []string{}, Timeout: 30 * time.Second}},
	}
	now := time.Now()
	def.CreatedAt, def.UpdatedAt = now, now
	if err := store.SaveWorkflowDefinition(ctx, def); err != nil {
		t.Fatalf("SaveWorkflowDefinition: %v", err)
	}
	// As if saved before 014: no revision yet.
	if _, err := store.Pool().Exec(ctx, `DELETE FROM workflow_revisions WHERE workflow_id = $1`, def.ID); err != nil {
		t.Fatal(err)
	}
	sql, err := fs.ReadFile(migrations.FS, "014_workflow_revisions.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := store.Pool().Exec(ctx, string(sql)); err != nil {
			t.Fatalf("running 014: %v", err)
		}
	}

	want, err := store.GetWorkflowDefinition(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetWorkflowRevision(ctx, def.ID, 1)
	if err != nil {
		t.Fatalf("GetWorkflowRevision: %v", err)
	}
	// Timestamps went through JSON; compare them as instants.
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
	got.CreatedAt, got.UpdatedAt, got.NextRunAt = want.CreatedAt, want.UpdatedAt, want.NextRunAt
	if !reflect.DeepEqual(got, want) {
		t.Errorf("backfilled revision = %+v\nwant %+v", got, want)
	}
	if _, err := store.GetWorkflowRevision(ctx, def.ID, 2); !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("revision 2 after a rerun: %v, want ErrNotFound", err)
	}
}
