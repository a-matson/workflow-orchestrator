package worker

import (
	"reflect"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func TestBuildCommand_ScriptAndModel(t *testing.T) {
	tests := []struct {
		name     string
		taskType string
		cfg      map[string]any
		want     []string
	}{
		{"data_transform script", "data_transform", map[string]any{"script": "echo hi | tr a-z A-Z"}, []string{"sh", "-c", "echo hi | tr a-z A-Z"}},
		{"generic script", "generic", map[string]any{"script": "echo hi | tr a-z A-Z"}, []string{"sh", "-c", "echo hi | tr a-z A-Z"}},
		{"ml_inference defaults", "ml_inference", map[string]any{"model_name": "model"}, []string{"model", "--batch-size", "32"}},
		{"ml_inference full", "ml_inference", map[string]any{"model_name": "model", "input_path": "in.csv", "output_path": "out.json", "batch_size": float64(8)},
			[]string{"model", "--batch-size", "8", "--input", "in.csv", "--output", "out.json"}},
		{"command and args still work", "generic", map[string]any{"command": "echo", "args": []any{"a", "b"}}, []string{"echo", "a", "b"}},
		{"command wins over script", "data_transform", map[string]any{"command": "echo", "script": "false"}, []string{"echo"}},
		{"nothing set", "generic", map[string]any{}, nil},
		{"ml_inference nothing set", "ml_inference", map[string]any{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCommand(&models.TaskMessage{TaskType: tc.taskType, Config: tc.cfg})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("buildCommand = %#v, want %#v", got, tc.want)
			}
		})
	}
}
