//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func getReady(t *testing.T, h *api.Handler) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/ready", nil)
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return rr.Code, body
}

func TestReady_ReportsDownDependency(t *testing.T) {
	store, redis := testutil.Env(t)

	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	code, body := getReady(t, api.NewHandler(store, redis, orch, api.NewHub(), nil))
	if code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("healthy: got %d %v, want 200 ready", code, body)
	}

	// Port 1 is never listening, so the PING fails fast.
	dead := persistence.NewRedisClient("127.0.0.1:1", "", 0)
	t.Cleanup(func() { _ = dead.Close() }) // best-effort close of a client that never connected
	code, body = getReady(t, api.NewHandler(store, dead, orch, api.NewHub(), nil))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("redis down: status %d, want 503", code)
	}
	checks, _ := body["checks"].(map[string]any)
	if checks["redis"] != "unavailable" || checks["postgres"] != "ok" {
		t.Fatalf("redis down: checks %v, want redis=unavailable postgres=ok", checks)
	}
}
