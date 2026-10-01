//go:build e2e

package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// F2 against the real stack: a signed POST with no API key starts a run, and
// the same request sent again is refused.
func TestE2E_WebhookStartsRun(t *testing.T) {
	do(t, http.MethodPut, baseURL+"/api/secrets/e2e_hook", map[string]any{"value": "hook-key-e2e"}, http.StatusOK, nil)
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name":    "e2e-webhook",
		"webhook": map[string]any{"secret": "e2e_hook"},
		"tasks":   []map[string]any{{"id": "a", "name": "A", "type": "generic", "dependencies": []string{}}},
	}, http.StatusCreated, &wf)

	body := []byte(`{"ref":"refs/heads/e2e"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("hook-key-e2e"))
	mac.Write([]byte(ts + "." + string(body)))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	post := func() (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/hooks/"+wf.ID, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fluxor-Timestamp", ts)
		req.Header.Set("X-Fluxor-Signature", sig)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Error(err)
			}
		}()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, raw
	}

	status, raw := post()
	if status != http.StatusAccepted {
		t.Fatalf("signed webhook = %d: %s", status, raw)
	}
	var started struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.ExecutionID == "" {
		t.Fatalf("response %s: want an execution_id (%v)", raw, err)
	}
	var exec struct {
		WorkflowID     string         `json:"workflow_id"`
		TriggerPayload map[string]any `json:"trigger_payload"`
	}
	do(t, http.MethodGet, baseURL+"/api/executions/"+started.ExecutionID, nil, http.StatusOK, &exec)
	if exec.WorkflowID != wf.ID || exec.TriggerPayload["ref"] != "refs/heads/e2e" {
		t.Errorf("execution = %+v, want workflow %s with the body as payload", exec, wf.ID)
	}
	if status, raw := post(); status != http.StatusUnauthorized {
		t.Errorf("replayed webhook = %d: %s, want 401", status, raw)
	}
	do(t, http.MethodDelete, baseURL+"/api/secrets/e2e_hook", map[string]any{}, http.StatusNoContent, nil)
}
