package worker

import (
	"strings"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func TestBuildEnv_ReservedPrefix(t *testing.T) {
	msg := &models.TaskMessage{
		TaskExecID: "real-exec", WorkflowExecID: "wf", TaskName: "t",
		Config: map[string]any{"env": map[string]any{
			"FLUXOR_WORKSPACE":    "/etc",
			"fluxor_task_exec_id": "evil",
			"FLUXOR_TASK_EXEC_ID": "evil",
			"FOO":                 "bar",
			"":                    "empty",
			"A=B":                 "eq",
			"NUL\x00":             "nul",
		}},
	}
	spec := models.ContainerSpec{Env: map[string]string{"Fluxor_Task_Name": "evil"}}

	got := map[string]int{}
	vals := map[string]string{}
	for _, kv := range buildEnv(msg, spec) {
		k, v, _ := strings.Cut(kv, "=")
		got[strings.ToUpper(k)]++
		vals[k] = v
	}
	for k, n := range got {
		if n != 1 {
			t.Errorf("key %q appears %d times", k, n)
		}
	}
	if vals["FLUXOR_WORKSPACE"] != WorkspaceDir {
		t.Errorf("FLUXOR_WORKSPACE = %q, want %q", vals["FLUXOR_WORKSPACE"], WorkspaceDir)
	}
	if vals["FLUXOR_TASK_EXEC_ID"] != "real-exec" {
		t.Errorf("FLUXOR_TASK_EXEC_ID = %q, want real-exec", vals["FLUXOR_TASK_EXEC_ID"])
	}
	if vals["FLUXOR_TASK_NAME"] != "t" {
		t.Errorf("FLUXOR_TASK_NAME = %q, want t", vals["FLUXOR_TASK_NAME"])
	}
	if vals["FOO"] != "bar" {
		t.Errorf("FOO = %q, want bar", vals["FOO"])
	}
	if len(vals) != 5 {
		t.Errorf("env = %v, want 4 FLUXOR_* keys plus FOO", vals)
	}
}
