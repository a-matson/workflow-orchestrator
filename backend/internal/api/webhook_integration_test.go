//go:build integration

package api_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/secrets"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// F2: a request signed with the workflow's webhook secret starts a run with
// its body as payload; anything else, including a replay, is a uniform 401.
func TestAPI_WebhookTrigger(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	box, err := secrets.NewBox(bytes.Repeat([]byte{2}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	vault := secrets.NewVault(store, box)
	if err := vault.Set(ctx, "hook_key", "s3cr3t"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	srv := api.NewHandler(store, redis, orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{}), api.NewHub(), nil).WithSecrets(vault).Server(nil)

	tasks := []models.TaskDefinition{{ID: "a", Name: "a", Type: "generic", Dependencies: []string{}}}
	hooked := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "Hooked", MaxParallel: 1, Tasks: tasks, Webhook: &models.WebhookTrigger{Secret: "hook_key"}}
	plain := &models.WorkflowDefinition{ID: uuid.NewString(), Name: "Plain", MaxParallel: 1, Tasks: tasks}
	testutil.SaveDef(t, store, hooked)
	testutil.SaveDef(t, store, plain)

	post := func(id string, body []byte, key string, ts time.Time) *httptest.ResponseRecorder {
		t.Helper()
		stamp := strconv.FormatInt(ts.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(stamp + "." + string(body)))
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/hooks/"+id, bytes.NewReader(body))
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fluxor-Timestamp", stamp)
		req.Header.Set("X-Fluxor-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}

	body := []byte(`{"ref":"refs/heads/main"}`)
	now := time.Now()
	rr := post(hooked.ID, body, "s3cr3t", now)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("signed webhook = %d: %s", rr.Code, rr.Body)
	}
	var started struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil || started.ExecutionID == "" {
		t.Fatalf("response %s: want an execution_id (%v)", rr.Body, err)
	}
	exec, err := store.GetWorkflowExecution(ctx, started.ExecutionID)
	if err != nil {
		t.Fatalf("GetWorkflowExecution: %v", err)
	}
	if exec.WorkflowID != hooked.ID || exec.TriggerPayload["ref"] != "refs/heads/main" {
		t.Errorf("execution = workflow %s payload %v, want %s with the body as payload", exec.WorkflowID, exec.TriggerPayload, hooked.ID)
	}

	for name, rr := range map[string]*httptest.ResponseRecorder{
		"replay":           post(hooked.ID, body, "s3cr3t", now),
		"wrong key":        post(hooked.ID, []byte(`{"n":1}`), "guess", now),
		"stale":            post(hooked.ID, []byte(`{"n":2}`), "s3cr3t", now.Add(-10*time.Minute)),
		"no webhook":       post(plain.ID, []byte(`{"n":3}`), "s3cr3t", now),
		"unknown workflow": post(uuid.NewString(), []byte(`{"n":4}`), "s3cr3t", now),
	} {
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, rr.Code)
		}
	}
}
