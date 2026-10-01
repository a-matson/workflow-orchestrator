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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	dockerclient "github.com/moby/moby/client"
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
	// `make e2e` mints this key and installs it as the stack's bootstrap admin key.
	apiKey = os.Getenv("FLUXOR_API_KEY")
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
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
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

func TestE2E_RequiresAuth(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/workflows", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/workflows without a key: status %d, want 401", resp.StatusCode)
	}
}

// The browser path: exchange the key for a cookie, then use only the cookie.
func TestE2E_BrowserSession(t *testing.T) {
	if apiKey == "" {
		t.Skip("FLUXOR_API_KEY is unset")
	}
	body, err := json.Marshal(map[string]string{"api_key": apiKey})
	if err != nil {
		t.Fatal(err)
	}
	login, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/session", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/session: status %d, want 200", resp.StatusCode)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "fluxor_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("POST /api/session set no fluxor_session cookie")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/workflows", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: session.Name, Value: session.Value})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/workflows with only the session cookie: status %d, want 200", resp.StatusCode)
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

// The UI reports errors it could not handle; the backend only logs them.
func TestE2E_ClientErrorReport(t *testing.T) {
	do(t, http.MethodPost, baseURL+"/api/client-errors", map[string]any{
		"message": "e2e", "stack": "Error: e2e", "source": "vue", "path": "/builder",
	}, http.StatusNoContent, nil)
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

// get returns the status and headers too, which send drops, for header assertions.
func get(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body of %s: %v", url, err)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

var scriptSrc = regexp.MustCompile(`src="(/assets/[^"]+\.js)"`)

// firstScriptAsset finds a hashed bundle through index.html so the tests survive rebuilds.
func firstScriptAsset(t *testing.T) string {
	t.Helper()
	_, _, index := get(t, frontendURL+"/")
	m := scriptSrc.FindSubmatch(index)
	if m == nil {
		t.Fatalf("index.html references no /assets/*.js:\n%s", index)
	}
	return string(m[1])
}

// nginx drops inherited add_header in any location that declares its own, so every
// location class (SPA, asset, proxied API) is probed, not just "/".
func TestFrontend_SecurityHeaders(t *testing.T) {
	for _, path := range []string{"/", "/executions", firstScriptAsset(t), "/api/health"} {
		_, h, _ := get(t, frontendURL+path)
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		// The proxied API is JSON, so the document-level headers are asserted on frontend paths only.
		if path != "/api/health" {
			if h.Get("Content-Security-Policy") == "" {
				t.Errorf("%s: no Content-Security-Policy", path)
			}
			if got := h.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("%s: X-Frame-Options = %q, want DENY", path, got)
			}
			if got := h.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
				t.Errorf("%s: Referrer-Policy = %q, want strict-origin-when-cross-origin", path, got)
			}
			if h.Get("Permissions-Policy") == "" {
				t.Errorf("%s: no Permissions-Policy", path)
			}
		}
		if server := h.Get("Server"); strings.ContainsAny(server, "0123456789/") {
			t.Errorf("%s: Server = %q leaks the nginx version", path, server)
		}
	}
}

func TestFrontend_NoSourceMaps(t *testing.T) {
	asset := firstScriptAsset(t)
	status, _, js := get(t, frontendURL+asset)
	if status != http.StatusOK {
		t.Fatalf("GET %s: %d", asset, status)
	}
	if strings.Contains(string(js), "sourceMappingURL") {
		t.Errorf("%s still carries a sourceMappingURL comment", asset)
	}
	status, _, _ = get(t, frontendURL+asset+".map")
	// SPA fallback would answer 200 with index.html, so only an explicit 404 passes.
	if status != http.StatusNotFound {
		t.Errorf("GET %s.map = %d, want 404", asset, status)
	}
}

func TestFrontend_Robots(t *testing.T) {
	status, h, body := get(t, frontendURL+"/robots.txt")
	if status != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Errorf("/robots.txt: status %d, type %q, want 200 text/plain:\n%s", status, h.Get("Content-Type"), body)
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
			ID           string
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
	// SEC-6: the browser downloads through the API, scoped to the task.
	artifacts := baseURL + "/api/tasks/" + got.Tasks[0].ID + "/artifacts/"
	if status, body := send(t, http.MethodGet, artifacts+"out.txt", nil); status != http.StatusOK || string(body) != "hello\n" {
		t.Errorf("GET task artifact out.txt = %d %q, want 200 %q", status, body, "hello\n")
	}
	if status, _ := send(t, http.MethodGet, artifacts+"missing.txt", nil); status != http.StatusNotFound {
		t.Errorf("GET task artifact missing.txt = %d, want 404", status)
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

// SEC-6: the presign endpoint signed any key, including other tasks' and
// non-artifact objects, for any lifetime. Downloads go through the task.
func TestE2E_PresignArbitraryKey(t *testing.T) {
	status, body := send(t, http.MethodGet, baseURL+"/api/artifacts/url?key=anything/at/all&expires=525600", nil)
	if status != http.StatusNotFound {
		t.Errorf("GET /api/artifacts/url = %d %s, want 404", status, body)
	}
}

// F4: config templates render the trigger payload in the worker, so the
// task's command runs with the payload's value.
func TestE2E_TemplateRendersPayload(t *testing.T) {
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name": "e2e-template",
		"tasks": []map[string]any{{
			"id": "run", "name": "Run", "type": "generic", "dependencies": []string{},
			"config":        map[string]any{"command": "sh", "args": []string{"-c", "echo {{ .payload.greeting }} > /workspace/out.txt"}},
			"container":     map[string]any{"image": "alpine:3.22"},
			"artifacts_out": []map[string]any{{"path": "out.txt"}},
		}},
	}, http.StatusCreated, &wf)
	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{"greeting": "hello-template"}, http.StatusAccepted, &run)

	var got struct {
		Status string
		Error  string
		Tasks  []struct {
			ArtifactsOut []struct {
				MinioKey string `json:"minio_key"`
			} `json:"artifacts_out"`
		}
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		got.Status = ""
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &got)
		if got.Status == "completed" || got.Status == "failed" || got.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %q after 120s", run.ID, got.Status)
		}
		time.Sleep(time.Second)
	}
	if got.Status != "completed" || len(got.Tasks) != 1 || len(got.Tasks[0].ArtifactsOut) != 1 {
		t.Fatalf("execution %s ended %q (%s)", run.ID, got.Status, got.Error)
	}
	if content := minioObject(t, got.Tasks[0].ArtifactsOut[0].MinioKey); content != "hello-template\n" {
		t.Errorf("artifact = %q, want the rendered payload value", content)
	}
}

// F3: a secret set through the API renders into a container task in the
// worker, and the task's logs show it masked.
func TestE2E_SecretRendersAndIsRedacted(t *testing.T) {
	do(t, http.MethodPut, baseURL+"/api/secrets/e2e_greeting", map[string]any{"value": "hello-secret-e2e"}, http.StatusOK, nil)
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name": "e2e-secret",
		"tasks": []map[string]any{{
			"id": "run", "name": "Run", "type": "generic", "dependencies": []string{},
			"config": map[string]any{"command": "sh", "args": []string{"-c",
				`echo {{ secret "e2e_greeting" }} > /workspace/out.txt; echo {{ secret "e2e_greeting" }}`}},
			"container":     map[string]any{"image": "alpine:3.22"},
			"artifacts_out": []map[string]any{{"path": "out.txt"}},
		}},
	}, http.StatusCreated, &wf)
	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &run)

	var got struct {
		Status string
		Error  string
		Tasks  []struct {
			ID           string
			ArtifactsOut []struct {
				MinioKey string `json:"minio_key"`
			} `json:"artifacts_out"`
		}
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		got.Status = ""
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &got)
		if got.Status == "completed" || got.Status == "failed" || got.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %q after 120s", run.ID, got.Status)
		}
		time.Sleep(time.Second)
	}
	if got.Status != "completed" || len(got.Tasks) != 1 || len(got.Tasks[0].ArtifactsOut) != 1 {
		t.Fatalf("execution %s ended %q (%s)", run.ID, got.Status, got.Error)
	}
	if content := minioObject(t, got.Tasks[0].ArtifactsOut[0].MinioKey); content != "hello-secret-e2e\n" {
		t.Errorf("artifact = %q, want the secret's value", content)
	}
	status, logs := send(t, http.MethodGet, baseURL+"/api/tasks/"+got.Tasks[0].ID+"/logs", nil)
	if status != http.StatusOK || strings.Contains(string(logs), "hello-secret-e2e") || !strings.Contains(string(logs), "***") {
		t.Errorf("task logs = %d %s, want the secret masked as ***", status, logs)
	}
	do(t, http.MethodDelete, baseURL+"/api/secrets/e2e_greeting", map[string]any{}, http.StatusNoContent, nil)
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

// runScriptArtifact runs script in an alpine task container and returns what it
// wrote to out.txt.
func runScriptArtifact(t *testing.T, name, script string) string {
	t.Helper()
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name": name,
		"tasks": []map[string]any{{
			"id": "run", "name": "Run", "type": "generic", "dependencies": []string{},
			"config":        map[string]any{"command": "sh", "args": []string{"-c", script}},
			"container":     map[string]any{"image": "alpine:3.22"},
			"artifacts_out": []map[string]any{{"path": "out.txt"}},
		}},
	}, http.StatusCreated, &wf)
	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &run)

	var got struct {
		Status string
		Error  string
		Tasks  []struct {
			ArtifactsOut []struct {
				MinioKey string `json:"minio_key"`
			} `json:"artifacts_out"`
		}
	}
	deadline := time.Now().Add(120 * time.Second) // the first run may pull the image
	for {
		got.Status = ""
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &got)
		if got.Status == "completed" || got.Status == "failed" || got.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %q after 120s", run.ID, got.Status)
		}
		time.Sleep(time.Second)
	}
	if got.Status != "completed" || len(got.Tasks) != 1 || len(got.Tasks[0].ArtifactsOut) != 1 {
		t.Fatalf("execution %s ended %q (%s), tasks = %+v", run.ID, got.Status, got.Error, got.Tasks)
	}
	return minioObject(t, got.Tasks[0].ArtifactsOut[0].MinioKey)
}

// A container escape or a writable-volume trick is far cheaper as root, so the
// task must run as the backend's own unprivileged uid:gid.
func TestE2E_TaskRunsAsNonRoot(t *testing.T) {
	got := strings.TrimSpace(runScriptArtifact(t, "e2e-nonroot", "echo $(id -u):$(id -g) > /workspace/out.txt"))
	uid, gid, ok := strings.Cut(got, ":")
	if !ok || uid == "0" || gid == "0" {
		t.Fatalf("task ran as %q, want a non-root uid:gid", got)
	}
	// 10001 is the appuser in backend/Dockerfile, which compose runs.
	if got != "10001:10001" {
		t.Errorf("task ran as %s, want the backend's 10001:10001", got)
	}
}

// Tasks that need the network use the http_request and notification types,
// which go through the egress guard; a shared bridge would let user code reach
// sibling task containers instead.
// Only eth* counts: the kernel gives every namespace unattached fallback tunnel
// devices (tunl0, gre0, ...) that carry no traffic.
func TestE2E_TaskHasNoNetwork(t *testing.T) {
	got := strings.Fields(runScriptArtifact(t, "e2e-no-network", "tail -n +3 /proc/net/dev | cut -d: -f1 > /workspace/out.txt"))
	for _, iface := range got {
		if strings.HasPrefix(iface, "eth") {
			t.Errorf("task has network interface %s (all: %v), want only loopback", iface, got)
		}
	}
}

// taskContainers lists the containers, running or not, that the executor
// created for task taskExecID.
func taskContainers(t *testing.T, dc *dockerclient.Client, taskExecID string) []string {
	t.Helper()
	res, err := dc.ContainerList(t.Context(), dockerclient.ContainerListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", "fluxor.task_exec_id="+taskExecID),
	})
	if err != nil {
		t.Fatalf("list containers of task %s: %v", taskExecID, err)
	}
	var states []string
	for _, c := range res.Items {
		states = append(states, c.ID[:12]+" "+string(c.State))
	}
	return states
}

// A cancelled execution must not leave user code running: until the
// container is gone it can still burn CPU and write side effects.
func TestE2E_CancelStopsRunningContainer(t *testing.T) {
	dc, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() {
		if err := dc.Close(); err != nil {
			t.Errorf("close docker client: %v", err)
		}
	})

	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name": "e2e-cancel-running",
		"tasks": []map[string]any{{
			"id": "sleep", "name": "Sleep", "type": "generic", "dependencies": []string{},
			"config":    map[string]any{"command": "sleep", "args": []string{"60"}},
			"container": map[string]any{"image": "alpine:3.22"},
		}},
	}, http.StatusCreated, &wf)
	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &run)

	// The row turns running before the image pull, so the container itself is
	// what proves the task's code has started.
	var taskID string
	deadline := time.Now().Add(120 * time.Second) // the first run may pull the image
	for {
		var got struct {
			Status string
			Tasks  []struct {
				ID     string
				Status string
			}
		}
		do(t, http.MethodGet, baseURL+"/api/executions/"+run.ID, nil, http.StatusOK, &got)
		if len(got.Tasks) == 1 && got.Tasks[0].Status == "running" {
			taskID = got.Tasks[0].ID
			if states := taskContainers(t, dc, taskID); len(states) == 1 && strings.HasSuffix(states[0], " running") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s (%q, tasks %+v) has no running task container after 120s", run.ID, got.Status, got.Tasks)
		}
		time.Sleep(500 * time.Millisecond)
	}

	do(t, http.MethodPost, baseURL+"/api/executions/"+run.ID+"/cancel", map[string]any{}, http.StatusOK, nil)

	deadline = time.Now().Add(5 * time.Second)
	for {
		states := taskContainers(t, dc, taskID)
		if len(states) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s containers %v still present 5s after cancel, want none", taskID, states)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
