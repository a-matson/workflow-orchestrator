package orchestrator

import (
	"slices"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/dag"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func TestReadyTasks(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	def := &models.WorkflowDefinition{Tasks: []models.TaskDefinition{
		{ID: "done"},
		{ID: "root"},
		{ID: "unblocked", Dependencies: []string{"done"}},
		{ID: "blocked", Dependencies: []string{"root"}},
		{ID: "retry-due"},
		{ID: "retry-later"},
		{ID: "queued"},
	}}
	g, err := dag.Parse(def)
	if err != nil {
		t.Fatal(err)
	}
	c := &ExecutionContext{Graph: g, Completed: map[string]bool{"done": true}, TaskMap: map[string]*models.TaskExecution{
		"done":        {Status: models.TaskStatusCompleted},
		"root":        {Status: models.TaskStatusPending},
		"unblocked":   {Status: models.TaskStatusPending},
		"blocked":     {Status: models.TaskStatusPending},
		"retry-due":   {Status: models.TaskStatusRetrying, NextRetryAt: &past},
		"retry-later": {Status: models.TaskStatusRetrying, NextRetryAt: &future},
		"queued":      {Status: models.TaskStatusQueued},
	}}

	got := c.readyTasks(now)
	slices.Sort(got)
	if want := []string{"retry-due", "root", "unblocked"}; !slices.Equal(got, want) {
		t.Errorf("readyTasks = %v, want %v", got, want)
	}
}
