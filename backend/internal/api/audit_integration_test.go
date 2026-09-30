//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

type auditEntry struct {
	ActorName  string `json:"actor_name"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	RequestID  string `json:"request_id"`
	Outcome    string `json:"outcome"`
}

func TestAPI_AuditTrail(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	srv := api.NewHandler(store, redis, orch, api.NewHub(), nil).Server(nil)

	def := &models.WorkflowDefinition{
		ID:          uuid.NewString(),
		Name:        "audited",
		Tasks:       []models.TaskDefinition{{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}}},
		MaxParallel: 1,
	}
	testutil.SaveDef(t, store, def)
	keyFor := func(name, role string) string {
		plain, _, err := store.CreateAPIKey(ctx, name, role)
		if err != nil {
			t.Fatalf("create %s key: %v", role, err)
		}
		return plain
	}
	operator, viewer, admin := keyFor("ops-key", "operator"), keyFor("view-key", "viewer"), keyFor("admin-key", "admin")

	call := func(method, path, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, nil)
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	trigger := "/api/workflows/" + def.ID + "/trigger"
	ok := call(http.MethodPost, trigger, operator)
	if ok.Code != http.StatusAccepted {
		t.Fatalf("operator trigger = %d: %s", ok.Code, ok.Body)
	}
	if rr := call(http.MethodPost, trigger, viewer); rr.Code != http.StatusForbidden {
		t.Fatalf("viewer trigger = %d, want 403", rr.Code)
	}

	if rr := call(http.MethodGet, "/api/audit", operator); rr.Code != http.StatusForbidden {
		t.Errorf("operator GET /api/audit = %d, want 403", rr.Code)
	}
	rr := call(http.MethodGet, "/api/audit", admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin GET /api/audit = %d: %s", rr.Code, rr.Body)
	}
	var body struct {
		Entries []auditEntry `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := map[string]auditEntry{
		"success": {ActorName: "ops-key", Action: "workflow.trigger", TargetType: "workflow", TargetID: def.ID, RequestID: ok.Header().Get("X-Request-ID"), Outcome: "success"},
		"denied":  {ActorName: "view-key", Action: "workflow.trigger", Outcome: "denied"},
	}
	for outcome, w := range want {
		found := false
		for _, e := range body.Entries {
			if e.Outcome != outcome {
				continue
			}
			found = true
			if outcome == "denied" {
				e.TargetType, e.TargetID, e.RequestID = "", "", ""
			}
			if e != w {
				t.Errorf("%s entry = %+v, want %+v", outcome, e, w)
			}
		}
		if !found {
			t.Errorf("no %s entry in %+v", outcome, body.Entries)
		}
	}
}
