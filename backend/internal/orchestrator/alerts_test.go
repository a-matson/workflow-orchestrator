package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func TestPostAlert(t *testing.T) {
	var got AlertPayload
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding alert: %v", err)
		}
	}))
	defer srv.Close()

	want := AlertPayload{Event: models.WSEventWorkflowFailed, ExecutionID: "e1", Status: models.WorkflowStatusFailed, FailedTasks: []string{"load"}}
	if err := postAlert(context.Background(), srv.Client(), srv.URL, want); err != nil {
		t.Fatalf("postAlert: %v", err)
	}
	if contentType != "application/json" || got.ExecutionID != "e1" || got.Event != "workflow.failed" || len(got.FailedTasks) != 1 {
		t.Errorf("receiver got %+v (%s)", got, contentType)
	}
}

// Webhook URLs often embed their credential, and postAlert's errors are logged.
func TestPostAlert_ErrorsOmitURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	token := "/hook/s3cr3t-token"
	err := postAlert(context.Background(), srv.Client(), srv.URL+token, AlertPayload{})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("want a 500 error, got %v", err)
	}
	srv.Close()
	err = postAlert(context.Background(), srv.Client(), srv.URL+token, AlertPayload{})
	if err == nil {
		t.Fatal("want an error from a closed receiver")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("error %q leaks the URL", err)
	}
}
