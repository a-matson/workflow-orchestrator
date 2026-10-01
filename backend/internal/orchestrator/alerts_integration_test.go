//go:build integration

package orchestrator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// F9: a completed run posts to on_success and a failed run to on_failure,
// under the definition the run started with.
func TestAlerts_PostedOnFinish(t *testing.T) {
	orch, store, redis, _ := setupOrchestrator(t)
	got := make(chan struct {
		path    string
		payload orchestrator.AlertPayload
	}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p orchestrator.AlertPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decoding alert: %v", err)
		}
		got <- struct {
			path    string
			payload orchestrator.AlertPayload
		}{r.URL.Path, p}
	}))
	defer srv.Close()
	orch.SetAlertClient(srv.Client())

	tasks := independentTasks("a")
	tasks[0].RetryPolicy = &models.RetryPolicy{MaxRetries: 0}
	def := &models.WorkflowDefinition{
		ID: uuid.NewString(), Name: "Alerts", MaxParallel: 1, Tasks: tasks,
		Alerts: &models.WorkflowAlerts{
			OnSuccess: &models.AlertTarget{URL: srv.URL + "/ok"},
			OnFailure: &models.AlertTarget{URL: srv.URL + "/fail"},
		},
	}
	testutil.SaveDef(t, store, def)
	stored, err := store.GetWorkflowDefinition(t.Context(), def.ID)
	if err != nil || stored.Alerts == nil || stored.Alerts.OnFailure.URL != srv.URL+"/fail" {
		t.Fatalf("stored alerts = %+v (%v), want them round-tripped", stored, err)
	}

	cases := []struct {
		path, event string
		status      models.WorkflowStatus
		failed      bool
	}{
		{"/ok", models.WSEventWorkflowCompleted, models.WorkflowStatusCompleted, false},
		{"/fail", models.WSEventWorkflowFailed, models.WorkflowStatusFailed, true},
	}
	for _, tc := range cases {
		exec := startWorkflow(t, orch, store, def)
		m := testutil.Drain(t, redis, 1)[0]
		res := testutil.Ok(m)
		if tc.failed {
			res = testutil.Fail(m, "boom")
		}
		runTask(t, orch, m, res)

		select {
		case a := <-got:
			if a.path != tc.path || a.payload.ExecutionID != exec.ID || a.payload.Status != tc.status || a.payload.Event != tc.event {
				t.Errorf("alert = %s %+v, want %s for %s (%s)", a.path, a.payload, tc.path, exec.ID, tc.status)
			}
			if tc.failed && (len(a.payload.FailedTasks) != 1 || a.payload.FailedTasks[0] != "a") {
				t.Errorf("failed_tasks = %v, want [a]", a.payload.FailedTasks)
			}
		case <-time.After(eventWait):
			t.Fatalf("no alert for the %s run", tc.status)
		}
	}
}
