package workflowyaml

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func TestRender_ThenParse_RoundTrips(t *testing.T) {
	next := time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)
	def := &models.WorkflowDefinition{
		ID: "server-id", Name: "ETL", Version: "1.0.0", MaxParallel: 4, Schedule: "0 2 * * *", NextRunAt: &next,
		GlobalRetry: &models.RetryPolicy{MaxRetries: 3, InitialDelay: 2 * time.Second, MaxDelay: 5 * time.Minute, BackoffMultiple: 2},
		Tasks: []models.TaskDefinition{
			{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}, Timeout: 90 * time.Second,
				Config: map[string]any{"command": "echo", "args": []any{"hi"}}},
			{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"},
				RetryPolicy: &models.RetryPolicy{MaxRetries: 1, InitialDelay: time.Second}},
		},
	}
	out, err := Render(def)
	if err != nil {
		t.Fatal(err)
	}
	y := string(out)
	for _, want := range []string{"timeout: 1m30s", "initial_delay: 2s", "max_delay: 5m0s", "name: ETL", "schedule: 0 2 * * *"} {
		if !strings.Contains(y, want) {
			t.Errorf("rendered YAML lacks %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "server-id") || strings.Contains(y, "created_at") || strings.Contains(y, "next_run_at") {
		t.Errorf("rendered YAML carries server fields:\n%s", y)
	}

	got, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(Render): %v\n%s", err, y)
	}
	if got.Name != "ETL" || len(got.Tasks) != 2 || got.Tasks[0].Timeout != 90*time.Second ||
		got.GlobalRetry.MaxDelay != 5*time.Minute || got.Tasks[1].RetryPolicy.InitialDelay != time.Second ||
		got.Tasks[1].Dependencies[0] != "a" || got.Tasks[0].Config["command"] != "echo" {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestParse_Rejects(t *testing.T) {
	for name, src := range map[string]string{
		"misspelled field": "name: x\ntaskz: []\n",
		"not a mapping":    "- a\n- b\n",
		"bad duration":     "name: x\ntasks:\n  - id: a\n    timeout: soon\n",
		"invalid yaml":     "name: [x\n",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: Parse accepted\n%s", name, src)
		}
	}
}

// The shipped example, converted to YAML and back, is the same definition.
func TestExample_RoundTrips(t *testing.T) {
	src, err := os.ReadFile("../../../examples/etl-pipeline.json")
	if err != nil {
		t.Fatal(err)
	}
	def, err := Parse(src) // JSON is valid YAML
	if err != nil {
		t.Fatalf("Parse(example json): %v", err)
	}
	out, err := Render(def)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(Render(example)): %v", err)
	}
	if len(back.Tasks) != len(def.Tasks) || back.Name != def.Name || back.GlobalRetry.InitialDelay != def.GlobalRetry.InitialDelay {
		t.Errorf("example changed in the round trip")
	}
}
