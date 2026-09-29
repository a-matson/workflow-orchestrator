package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func task(id string, deps ...string) models.TaskDefinition {
	return models.TaskDefinition{ID: id, Dependencies: deps}
}

func TestValidateDefinition(t *testing.T) {
	withIn := task("a")
	withIn.ArtifactsIn = []models.ArtifactRef{{Path: "/etc/passwd"}}
	withOut := task("a")
	withOut.ArtifactsOut = []models.ArtifactRef{{Path: "../../etc/passwd"}}
	emptyPath := task("a")
	emptyPath.ArtifactsOut = []models.ArtifactRef{{Path: ""}}
	okPath := task("a")
	okPath.ArtifactsOut = []models.ArtifactRef{{Path: "results/data.csv"}}

	tests := []struct {
		name    string
		tasks   []models.TaskDefinition
		wantErr string // substring; empty means valid
	}{
		{"valid dag", []models.TaskDefinition{task("a"), task("b", "a")}, ""},
		{"valid artifact path", []models.TaskDefinition{okPath}, ""},
		{"cycle", []models.TaskDefinition{task("a", "b"), task("b", "a")}, "cycle"},
		{"unknown dependency", []models.TaskDefinition{task("a", "ghost")}, "depends on unknown task ghost"},
		{"duplicate id", []models.TaskDefinition{task("a"), task("a")}, "duplicate task ID: a"},
		{"empty tasks", nil, "no tasks"},
		{"id with space", []models.TaskDefinition{task("a b")}, "id must be non-empty and match"},
		{"id with slash", []models.TaskDefinition{task("a/b")}, "id must be non-empty and match"},
		{"empty id", []models.TaskDefinition{task("")}, "id must be non-empty and match"},
		{"id too long", []models.TaskDefinition{task(strings.Repeat("a", 65))}, "too long"},
		{"absolute artifact in", []models.TaskDefinition{withIn}, "artifact path \"/etc/passwd\""},
		{"dotdot artifact out", []models.TaskDefinition{withOut}, "artifact path \"../../etc/passwd\""},
		{"empty artifact path", []models.TaskDefinition{emptyPath}, "artifact path \"\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDefinition(&models.WorkflowDefinition{Tasks: tc.tasks})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// The shipped example must keep validating as rules tighten.
func TestValidateDefinitionExampleETL(t *testing.T) {
	b, err := os.ReadFile("../../../examples/etl-pipeline.json")
	if err != nil {
		t.Fatal(err)
	}
	var def models.WorkflowDefinition
	if err := json.Unmarshal(b, &def); err != nil {
		t.Fatal(err)
	}
	if err := validateDefinition(&def); err != nil {
		t.Fatalf("etl-pipeline.json rejected: %v", err)
	}
}
