//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const ghaWorkflowYAML = `name: e2e-gha
jobs:
  build:
    runs-on: ubuntu-latest
    container: alpine:3.22
    steps:
      - name: Compile
        run: echo "compiled with $TOKEN"
    env:
      TOKEN: ${{ secrets.e2e_gha_token }}
  check:
    needs: build
    runs-on: ubuntu-latest
    container: alpine:3.22
    steps:
      - name: Greet
        run: echo "hello from ${{ env.WHO }}"
        env:
          WHO: check
`

// F13 against the real stack: a GitHub Actions workflow imports as container
// tasks that run in order, with the secret resolved and masked in the logs.
func TestE2E_GitHubActionsImportRuns(t *testing.T) {
	do(t, http.MethodPut, baseURL+"/api/secrets/e2e_gha_token", map[string]any{"value": "gha-secret-e2e"}, http.StatusOK, nil)
	var wf struct {
		ID    string
		Tasks []struct{ ID string }
	}
	do(t, http.MethodPost, baseURL+"/api/workflows/import", map[string]any{"yaml": ghaWorkflowYAML}, http.StatusCreated, &wf)
	if len(wf.Tasks) != 2 {
		t.Fatalf("imported %d tasks, want 2", len(wf.Tasks))
	}
	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &run)

	var got struct {
		Status string
		Error  string
		Tasks  []struct {
			ID               string
			TaskDefinitionID string `json:"task_definition_id"`
			Status           string
		}
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &got)
		if got.Status == "completed" || got.Status == "failed" || got.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %q after 120s", run.ID, got.Status)
		}
		time.Sleep(time.Second)
	}
	if got.Status != "completed" {
		t.Fatalf("execution %s ended %q (%s)", run.ID, got.Status, got.Error)
	}
	want := map[string]string{"build": "== Compile", "check": "hello from check"}
	for _, task := range got.Tasks {
		status, logs := send(t, http.MethodGet, baseURL+"/api/tasks/"+task.ID+"/logs", nil)
		if status != http.StatusOK || !strings.Contains(string(logs), want[task.TaskDefinitionID]) {
			t.Errorf("task %s logs = %d %s, want %q", task.TaskDefinitionID, status, logs, want[task.TaskDefinitionID])
		}
		if strings.Contains(string(logs), "gha-secret-e2e") {
			t.Errorf("task %s logs leak the secret: %s", task.TaskDefinitionID, logs)
		}
	}
	do(t, http.MethodDelete, baseURL+"/api/secrets/e2e_gha_token", map[string]any{}, http.StatusNoContent, nil)
}
