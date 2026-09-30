package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// Without a container executor, code task types must fail closed instead of
// running user code on the backend host.
func TestDispatch_RefusesShellTypesWithoutContainers(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.sh")
	modelMarker := filepath.Join(dir, "ml_inference")
	if err := os.WriteFile(model, []byte("#!/bin/sh\ntouch "+modelMarker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		taskType string
		config   map[string]any
		marker   string
	}{
		{"data_transform", map[string]any{"script": "touch " + filepath.Join(dir, "data_transform")}, filepath.Join(dir, "data_transform")},
		{"generic", map[string]any{"command": "touch", "args": []any{filepath.Join(dir, "generic")}}, filepath.Join(dir, "generic")},
		{"ml_inference", map[string]any{"model_name": model}, modelMarker},
		// Unknown types used to fall through to the generic shell executor.
		{"custom", map[string]any{"command": "touch", "args": []any{filepath.Join(dir, "custom")}}, filepath.Join(dir, "custom")},
	}
	for _, tt := range tests {
		t.Run(tt.taskType, func(t *testing.T) {
			w := &Worker{}
			msg := &models.TaskMessage{TaskType: tt.taskType, Config: tt.config}
			noLog := func(string, string, map[string]any) {}

			_, _, err := w.dispatch(context.Background(), msg, noLog)

			if _, statErr := os.Stat(tt.marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("user code ran on the host: marker %s exists (stat err %v)", tt.marker, statErr)
			}
			if err == nil || !strings.Contains(err.Error(), "container runtime unavailable") {
				t.Errorf("dispatch error = %v, want container runtime unavailable", err)
			}
		})
	}
}

// A generic task with nothing to run is a no-op; saved demo templates and the
// e2e suite rely on it, and it runs no user code, so it needs no container.
func TestDispatch_GenericWithoutCommandIsNoop(t *testing.T) {
	w := &Worker{}
	msg := &models.TaskMessage{TaskType: "generic", Config: map[string]any{"train_ratio": 0.8}}

	out, _, err := w.dispatch(context.Background(), msg, func(string, string, map[string]any) {})

	if err != nil || out["status"] != "no-op" {
		t.Fatalf("dispatch = %v, %v; want no-op", out, err)
	}
}
