//go:build integration

package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/secrets"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// F3: admins set, list and delete secrets; a value never comes back out, and
// it is stored sealed.
func TestAPI_Secrets(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	box, err := secrets.NewBox(bytes.Repeat([]byte{1}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	srv := api.NewHandler(store, redis, orch, api.NewHub(), nil).WithSecrets(secrets.NewVault(store, box)).Server(nil)
	plain := api.NewHandler(store, redis, orch, api.NewHub(), nil).Server(nil)
	admin, _, err := store.CreateAPIKey(ctx, "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	do := func(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Host = "localhost"
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := do(srv, http.MethodPut, "/api/secrets/db_password", `{"value":"hunter2"}`); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "hunter2") {
		t.Fatalf("PUT = %d %s", rr.Code, rr.Body)
	}
	if rr := do(srv, http.MethodGet, "/api/secrets", ""); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "db_password") || strings.Contains(rr.Body.String(), "hunter2") {
		t.Errorf("GET list = %d %s, want the name and no value", rr.Code, rr.Body)
	}
	var stored []byte
	if err := store.Pool().QueryRow(ctx, `SELECT ciphertext FROM secrets WHERE name = 'db_password'`).Scan(&stored); err != nil || bytes.Contains(stored, []byte("hunter2")) {
		t.Errorf("stored ciphertext %q (err %v) contains the plaintext", stored, err)
	}
	if rr := do(srv, http.MethodPut, "/api/secrets/bad%20name", `{"value":"x"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("bad name = %d, want 400", rr.Code)
	}
	if rr := do(srv, http.MethodDelete, "/api/secrets/db_password", ""); rr.Code != http.StatusNoContent {
		t.Errorf("DELETE = %d", rr.Code)
	}
	if rr := do(srv, http.MethodDelete, "/api/secrets/db_password", ""); rr.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", rr.Code)
	}
	if rr := do(plain, http.MethodGet, "/api/secrets", ""); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("without a key, GET = %d, want 503", rr.Code)
	}
}
