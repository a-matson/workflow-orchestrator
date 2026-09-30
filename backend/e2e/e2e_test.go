//go:build e2e

// Package e2e drives the running compose stack over HTTP (`make e2e`).
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	baseURL     = env("FLUXOR_BASE_URL", "http://localhost:8080")
	frontendURL = env("FLUXOR_FRONTEND_URL", "http://localhost:3000")
	client      = &http.Client{Timeout: 10 * time.Second}
)

// send issues the request, sending body as JSON because the API rejects
// mutating requests without that Content-Type.
func send(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// do requires wantStatus and decodes the JSON response into out, if non-nil.
func do(t *testing.T, method, url string, body any, wantStatus int, out any) {
	t.Helper()
	status, raw := send(t, method, url, body)
	if status != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, url, status, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode: %v: %s", method, url, err, raw)
		}
	}
}

func TestE2E_Health(t *testing.T) {
	do(t, http.MethodGet, baseURL+"/api/health", nil, http.StatusOK, nil)
	do(t, http.MethodGet, baseURL+"/api/ready", nil, http.StatusOK, nil)
}

// A generic task without a command is a no-op in the worker, so this workflow
// completes without any external service or container runtime.
func TestE2E_NoopWorkflowCompletes(t *testing.T) {
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name":  "e2e-noop",
		"tasks": []map[string]any{{"id": "noop", "name": "No-op", "type": "generic", "dependencies": []string{}}},
	}, http.StatusCreated, &wf)

	var exec struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &exec)

	type execution struct {
		Status string
		Error  string
		Tasks  []struct {
			TaskName string `json:"task_name"`
			Status   string
		}
	}
	var got execution
	deadline := time.Now().Add(60 * time.Second)
	for {
		got = execution{}
		do(t, http.MethodGet, baseURL+"/api/executions/"+exec.ID, nil, http.StatusOK, &got)
		if got.Status == "completed" || got.Status == "failed" || got.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %q after 60s", exec.ID, got.Status)
		}
		time.Sleep(time.Second)
	}
	if got.Status != "completed" {
		t.Fatalf("execution %s ended %q: %s", exec.ID, got.Status, got.Error)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Status != "completed" {
		t.Fatalf("tasks = %+v, want one completed task", got.Tasks)
	}
}

// Every client-side route must fall back to the SPA so refreshes and deep links work.
func TestE2E_FrontendServesSPA(t *testing.T) {
	for _, path := range []string{"/", "/executions", "/metrics"} {
		status, body := send(t, http.MethodGet, frontendURL+path, nil)
		if status != http.StatusOK || !strings.Contains(string(body), `<div id="app">`) {
			t.Errorf("GET %s%s: status %d, body lacks the SPA mount:\n%s", frontendURL, path, status, body)
		}
	}
}

// The backend drives the host daemon through its socket, so the task's
// /workspace must be something both it and the task container can reach.
func TestE2E_ContainerTaskWritesArtifact(t *testing.T) {
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name": "e2e-container-artifact",
		"tasks": []map[string]any{{
			"id": "write", "name": "Write", "type": "generic", "dependencies": []string{},
			"config":        map[string]any{"command": "sh", "args": []string{"-c", "echo hello > /workspace/out.txt"}},
			"container":     map[string]any{"image": "alpine:3.22"},
			"artifacts_out": []map[string]any{{"path": "out.txt"}},
		}},
	}, http.StatusCreated, &wf)

	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &run)

	type execution struct {
		Status string
		Error  string
		Tasks  []struct {
			Status       string
			Error        string
			ArtifactsOut []struct {
				Path     string
				MinioKey string `json:"minio_key"`
			} `json:"artifacts_out"`
		}
	}
	var got execution
	// Generous: the first run may pull the image.
	deadline := time.Now().Add(120 * time.Second)
	for {
		got = execution{}
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &got)
		if got.Status == "completed" || got.Status == "failed" || got.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %q after 120s", run.ID, got.Status)
		}
		time.Sleep(time.Second)
	}
	if got.Status != "completed" || len(got.Tasks) != 1 {
		t.Fatalf("execution %s ended %q (%s), tasks = %+v", run.ID, got.Status, got.Error, got.Tasks)
	}
	arts := got.Tasks[0].ArtifactsOut
	if len(arts) != 1 || arts[0].Path != "out.txt" {
		t.Fatalf("artifacts_out = %+v, want out.txt", arts)
	}
	if content := minioObject(t, arts[0].MinioKey); content != "hello\n" {
		t.Errorf("artifact content = %q, want %q", content, "hello\n")
	}

	// The worker removes the workspace before it reports the result.
	vol := env("FLUXOR_WORKSPACE_VOLUME", "fluxor-e2e-task-workspaces")
	out, err := exec.CommandContext(t.Context(), "docker", "run", "--rm", "-v", vol+":/w:ro", "alpine:3.22", "ls", "-A", "/w").CombinedOutput()
	if err != nil {
		t.Fatalf("list volume %s: %v: %s", vol, err, out)
	}
	if len(out) != 0 {
		t.Errorf("workspace volume %s not empty after the task:\n%s", vol, out)
	}
}

// minioObject reads an object with the credentials `make e2e` gives the stack.
func minioObject(t *testing.T, key string) string {
	t.Helper()
	mc, err := minio.New(env("MINIO_ENDPOINT", "localhost:9000"), &minio.Options{
		Creds: credentials.NewStaticV4(env("MINIO_ROOT_USER", "fluxor"), env("MINIO_ROOT_PASSWORD", "change-me-minio"), ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := mc.GetObject(t.Context(), "fluxor-artifacts", key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	b, err := io.ReadAll(obj)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if err := obj.Close(); err != nil {
		t.Fatal(err)
	}
	return string(b)
}
