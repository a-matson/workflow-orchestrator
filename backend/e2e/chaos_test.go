//go:build e2e && chaos

package e2e

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	dockerclient "github.com/moby/moby/client"
)

// backendContainer is the backend of the e2e compose project (`make chaos`).
var backendContainer = env("FLUXOR_CHAOS_CONTAINER", "fluxor-e2e-backend-1")

// Restarts kill every in-process worker, so recovery and the pickup and
// result guards must still finish each task exactly once: the run completes,
// no row is left open, no attempt is spent on a restart, and no task
// container outlives the process that started it.
func TestChaos_RestartsDuringDAG(t *testing.T) {
	const layers, width, restarts = 10, 1, 20
	var tasks []map[string]any
	for l := range layers {
		for w := range width {
			var deps []string
			if l > 0 {
				for p := range width {
					deps = append(deps, fmt.Sprintf("t%d%d", l-1, p))
				}
			}
			tasks = append(tasks, map[string]any{
				"id": fmt.Sprintf("t%d%d", l, w), "name": fmt.Sprintf("T%d%d", l, w), "type": "generic",
				"dependencies": deps,
				"config":       map[string]any{"command": "sleep", "args": []string{"2"}},
				"container":    map[string]any{"image": "alpine:3.22"},
			})
		}
	}
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{"name": "chaos-restarts", "tasks": tasks}, http.StatusCreated, &wf)
	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &run)

	midRun := 0
	for i := range restarts {
		// Varied so kills land on pickups, runs and result processing alike.
		time.Sleep(time.Duration(500+(i*700)%3000) * time.Millisecond)
		var st struct{ Status string }
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &st)
		if st.Status == "running" {
			midRun++
		}
		docker(t, "kill", "--signal", "KILL", backendContainer)
		docker(t, "start", backendContainer)
		waitReady(t)
	}
	t.Logf("%d of %d restarts hit the running execution", midRun, restarts)
	// A DAG that finishes early leaves the later restarts testing nothing.
	if midRun < restarts/2 {
		t.Fatalf("only %d of %d restarts hit the running execution; lengthen the DAG", midRun, restarts)
	}

	type execution struct {
		Status string
		Error  string
		Tasks  []struct {
			ID               string
			TaskDefinitionID string `json:"task_definition_id"`
			Status           string
			RetryCount       int `json:"retry_count"`
		}
	}
	var got execution
	deadline := time.Now().Add(5 * time.Minute)
	for {
		got = execution{}
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &got)
		if got.Status != "running" && got.Status != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %q 5m after the last restart; tasks %+v", run.ID, got.Status, got.Tasks)
		}
		time.Sleep(time.Second)
	}
	if got.Status != "completed" {
		t.Fatalf("execution %s ended %q: %s", run.ID, got.Status, got.Error)
	}
	if len(got.Tasks) != layers*width {
		t.Fatalf("execution has %d tasks, want %d", len(got.Tasks), layers*width)
	}

	dc, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() {
		if err := dc.Close(); err != nil {
			t.Errorf("close docker client: %v", err)
		}
	})
	for _, task := range got.Tasks {
		if task.Status != "completed" || task.RetryCount != 0 {
			t.Errorf("task %s = %s at retry %d, want completed at retry 0", task.TaskDefinitionID, task.Status, task.RetryCount)
		}
		if left := taskContainers(t, dc, task.ID); len(left) != 0 {
			t.Errorf("task %s left containers %v", task.TaskDefinitionID, left)
		}
	}
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/ready", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			ready := resp.StatusCode == http.StatusOK
			if err := resp.Body.Close(); err != nil {
				t.Errorf("close ready body: %v", err)
			}
			if ready {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend not ready 90s after a restart")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
