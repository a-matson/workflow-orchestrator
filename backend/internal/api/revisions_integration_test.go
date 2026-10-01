//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// F8: every save is a new revision, and an old revision reads back as saved.
func TestAPI_WorkflowRevisions(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	srv := api.NewHandler(store, redis, orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{}), api.NewHub(), nil).Server(nil)
	operator, _, err := store.CreateAPIKey(ctx, "operator", "operator")
	if err != nil {
		t.Fatalf("create operator key: %v", err)
	}
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequestWithContext(ctx, method, path, &buf)
		req.Host = "localhost"
		req.Header.Set("Authorization", "Bearer "+operator)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}

	def := models.WorkflowDefinition{Name: "First", MaxParallel: 1, Tasks: []models.TaskDefinition{{ID: "a", Name: "a", Type: "generic", Dependencies: []string{}}}}
	rr := do(http.MethodPost, "/api/workflows", def)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	def.Name = "Second"
	if rr := do(http.MethodPut, "/api/workflows/"+def.ID, def); rr.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rr.Code, rr.Body)
	}

	rr = do(http.MethodGet, "/api/workflows/"+def.ID+"/revisions", nil)
	var list struct {
		Revisions []models.WorkflowRevision `json:"revisions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("list = %d %s (%v)", rr.Code, rr.Body, err)
	}
	if len(list.Revisions) != 2 || list.Revisions[0].Revision != 2 || list.Revisions[1].Revision != 1 {
		t.Fatalf("revisions = %+v, want 2 then 1", list.Revisions)
	}

	rr = do(http.MethodGet, "/api/workflows/"+def.ID+"/revisions/1", nil)
	var first models.WorkflowDefinition
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("get revision 1 = %d %s (%v)", rr.Code, rr.Body, err)
	}
	if first.Name != "First" || first.ID != def.ID || len(first.Tasks) != 1 {
		t.Errorf("revision 1 = %+v, want the definition as first saved", first)
	}

	for path, want := range map[string]int{
		"/api/workflows/" + def.ID + "/revisions/3":   http.StatusNotFound,
		"/api/workflows/" + def.ID + "/revisions/0":   http.StatusBadRequest,
		"/api/workflows/" + def.ID + "/revisions/abc": http.StatusBadRequest,
		"/api/workflows/missing/revisions":            http.StatusNotFound,
	} {
		if rr := do(http.MethodGet, path, nil); rr.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rr.Code, want)
		}
	}
}
