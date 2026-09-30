//go:build integration

package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func TestAPI_RequestBodyLimit(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	srv := api.NewHandler(store, redis, orch, api.NewHub(), nil).Server(nil)

	operator, _, err := store.CreateAPIKey(ctx, "operator", "operator")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	def := &models.WorkflowDefinition{
		ID:          uuid.NewString(),
		Name:        "limits",
		Tasks:       []models.TaskDefinition{{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}}},
		MaxParallel: 1,
	}
	testutil.SaveDef(t, store, def)

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+operator)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	workflow := func(padBytes int) string {
		return `{"name":"big","description":"` + strings.Repeat("x", padBytes) +
			`","max_parallel":1,"tasks":[{"id":"a","name":"A","type":"generic","dependencies":[]}]}`
	}
	const elevenMiB = 11 << 20
	trigger := "/api/workflows/" + def.ID + "/trigger"

	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"11 MiB workflow", "/api/workflows", workflow(elevenMiB), http.StatusRequestEntityTooLarge},
		{"11 MiB trigger payload", trigger, `{"pad":"` + strings.Repeat("x", elevenMiB) + `"}`, http.StatusRequestEntityTooLarge},
		{"100 KiB workflow", "/api/workflows", workflow(100 << 10), http.StatusCreated},
	} {
		rr := post(tc.path, tc.body)
		if rr.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, rr.Code, tc.want)
			continue
		}
		if tc.want == http.StatusRequestEntityTooLarge && !strings.Contains(rr.Body.String(), `"request body too large"`) {
			t.Errorf("%s: body = %s", tc.name, rr.Body)
		}
	}
}
