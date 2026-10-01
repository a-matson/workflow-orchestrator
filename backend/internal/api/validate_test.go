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
	badRule := task("a")
	badRule.TriggerRule = "sometimes"
	okRule := task("b", "a")
	okRule.TriggerRule = models.TriggerRuleOneFailed
	badTemplate := task("a")
	badTemplate.Config = map[string]any{"url": "{{ .payload.x"}
	okPath := task("a")
	okPath.ArtifactsOut = []models.ArtifactRef{{Path: "results/data.csv"}}
	// Not canonical: a consumer's "./out.txt" never matches a producer's
	// "out.txt", and the mux redirects such a download URL to its clean form.
	dotSlash := task("a")
	dotSlash.ArtifactsIn = []models.ArtifactRef{{Path: "./out.txt"}}
	doubleSlash := task("a")
	doubleSlash.ArtifactsOut = []models.ArtifactRef{{Path: "results//data.csv"}}
	trailingSlash := task("a")
	trailingSlash.ArtifactsOut = []models.ArtifactRef{{Path: "results/"}}

	tests := []struct {
		name    string
		tasks   []models.TaskDefinition
		wantErr string // substring; empty means valid
	}{
		{"valid dag", []models.TaskDefinition{task("a"), task("b", "a")}, ""},
		{"valid trigger rule", []models.TaskDefinition{task("a"), okRule}, ""},
		{"unknown trigger rule", []models.TaskDefinition{badRule}, "trigger_rule \"sometimes\""},
		{"broken config template", []models.TaskDefinition{badTemplate}, "config: template"},
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
		{"dot-slash artifact path", []models.TaskDefinition{dotSlash}, "artifact path \"./out.txt\""},
		{"double-slash artifact path", []models.TaskDefinition{doubleSlash}, "artifact path \"results//data.csv\""},
		{"trailing-slash artifact path", []models.TaskDefinition{trailingSlash}, "artifact path \"results/\""},
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

func TestValidateDefinition_Schedule(t *testing.T) {
	ok := &models.WorkflowDefinition{Tasks: []models.TaskDefinition{task("a")}, Schedule: "0 2 * * *"}
	if err := validateDefinition(ok); err != nil {
		t.Errorf("valid schedule rejected: %v", err)
	}
	bad := &models.WorkflowDefinition{Tasks: []models.TaskDefinition{task("a")}, Schedule: "every day"}
	if err := validateDefinition(bad); err == nil || !strings.Contains(err.Error(), `schedule "every day"`) {
		t.Errorf("bad schedule: %v, want an error naming it", err)
	}
}

func TestValidateDefinition_When(t *testing.T) {
	bad := task("a")
	bad.When = "{{ eq .payload.env"
	if err := validateDefinition(&models.WorkflowDefinition{Tasks: []models.TaskDefinition{bad}}); err == nil || !strings.Contains(err.Error(), "when:") {
		t.Errorf("broken when template: %v, want an error naming when", err)
	}
	withSecret := task("a")
	withSecret.When = `{{ eq (secret "flag") "on" }}`
	if err := validateDefinition(&models.WorkflowDefinition{Tasks: []models.TaskDefinition{withSecret}}); err == nil || !strings.Contains(err.Error(), "when:") {
		t.Errorf("when calling secret: %v, want it rejected (when renders outside the worker)", err)
	}
}

func TestValidateDefinitionAlerts(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https", "https://hooks.example.com/T0/abc", false},
		{"http", "http://alerts.internal:8080/fluxor", false},
		{"no scheme", "hooks.example.com/abc", true},
		{"other scheme", "file:///etc/passwd", true},
		{"no host", "https:///abc", true},
		{"empty", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			def := &models.WorkflowDefinition{
				Tasks:  []models.TaskDefinition{task("a")},
				Alerts: &models.WorkflowAlerts{OnFailure: &models.AlertTarget{URL: tc.url}},
			}
			err := validateDefinition(def)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && tc.url != "" && strings.Contains(err.Error(), tc.url) {
				t.Errorf("error %q echoes the URL, which may carry a token", err)
			}
		})
	}
}
