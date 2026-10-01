//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

const importYAML = `name: Imported
max_parallel: 2
tasks:
  - id: fetch
    name: Fetch
    type: generic
    dependencies: []
    timeout: 45s
  - id: load
    name: Load
    type: generic
    dependencies: [fetch]
    retry_policy:
      max_retries: 2
      initial_delay: 1s
`

func TestAPI_ImportThenExportYAML(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	srv := api.NewHandler(store, redis, orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{}), api.NewHub(), nil).Server(nil)
	operator, _, err := store.CreateAPIKey(ctx, "operator", "operator")
	if err != nil {
		t.Fatalf("create operator key: %v", err)
	}
	do := func(method, path string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
		req.Host = "localhost"
		req.Header.Set("Authorization", "Bearer "+operator)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	body, err := json.Marshal(map[string]string{"yaml": importYAML})
	if err != nil {
		t.Fatal(err)
	}

	rr := do(http.MethodPost, "/api/workflows/import", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("import = %d: %s", rr.Code, rr.Body)
	}
	var created models.WorkflowDefinition
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "Imported" || len(created.Tasks) != 2 || created.Tasks[0].Timeout != 45*time.Second {
		t.Fatalf("imported definition = %+v", created)
	}

	rr = do(http.MethodGet, "/api/workflows/"+created.ID+"/export", nil)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/yaml" {
		t.Fatalf("export = %d %q: %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body)
	}
	exported, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "timeout: 45s") || strings.Contains(string(exported), created.ID) {
		t.Errorf("export lacks the duration string or carries the id:\n%s", exported)
	}

	// The export imports again, as a new workflow.
	again, err := json.Marshal(map[string]string{"yaml": string(exported)})
	if err != nil {
		t.Fatal(err)
	}
	if rr := do(http.MethodPost, "/api/workflows/import", again); rr.Code != http.StatusCreated || strings.Contains(rr.Body.String(), created.ID) {
		t.Errorf("re-import = %d: %s", rr.Code, rr.Body)
	}

	for name, src := range map[string]string{
		"unknown field": "name: x\ntaskz: []\n",
		"cycle":         "name: x\ntasks:\n  - {id: a, name: A, type: generic, dependencies: [b]}\n  - {id: b, name: B, type: generic, dependencies: [a]}\n",
	} {
		b, err := json.Marshal(map[string]string{"yaml": src})
		if err != nil {
			t.Fatal(err)
		}
		if rr := do(http.MethodPost, "/api/workflows/import", b); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: import = %d, want 400: %s", name, rr.Code, rr.Body)
		}
	}
}
