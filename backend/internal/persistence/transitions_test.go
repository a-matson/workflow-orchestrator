package persistence_test

import (
	"context"
	"errors"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// The target check runs before any query, so a Store without a pool is enough.
func TestTransition_InvalidTarget(t *testing.T) {
	s := new(persistence.Store)
	ctx := context.Background()
	for _, to := range []models.TaskStatus{models.TaskStatusFailed, models.TaskStatusSkipped} {
		if _, err := s.TransitionTask(ctx, "t", 0, to, persistence.TaskPatch{}); !errors.Is(err, persistence.ErrInvalidTransition) {
			t.Errorf("TransitionTask(%s) error = %v, want ErrInvalidTransition", to, err)
		}
	}
	if _, err := s.TransitionExecution(ctx, "e", models.WorkflowStatusPaused, ""); !errors.Is(err, persistence.ErrInvalidTransition) {
		t.Errorf("TransitionExecution(paused) error = %v, want ErrInvalidTransition", err)
	}
}
