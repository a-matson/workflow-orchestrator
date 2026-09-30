//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func TestAPI_RequiresAuth(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	orch := orchestrator.NewOrchestrator(store, redis, &testutil.Recorder{})
	srv := api.NewHandler(store, redis, orch, api.NewHub(), nil).Server(nil)

	def := &models.WorkflowDefinition{
		ID:          uuid.NewString(),
		Name:        "auth",
		Tasks:       []models.TaskDefinition{{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}}},
		MaxParallel: 1,
	}
	testutil.SaveDef(t, store, def)

	key := func(name, role string) string {
		plain, _, err := store.CreateAPIKey(ctx, name, role)
		if err != nil {
			t.Fatalf("create %s key: %v", role, err)
		}
		return plain
	}
	viewer, operator := key("viewer", "viewer"), key("operator", "operator")
	revoked, rk, err := store.CreateAPIKey(ctx, "revoked", "admin")
	if err != nil {
		t.Fatalf("create revoked key: %v", err)
	}
	if err := store.RevokeAPIKey(ctx, rk.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	call := func(method, path, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader("{}"))
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	trigger := "/api/workflows/" + def.ID + "/trigger"

	for _, tc := range []struct {
		name, method, path, key string
		want                    int
	}{
		{"no key", http.MethodGet, "/api/workflows", "", http.StatusUnauthorized},
		{"malformed key", http.MethodGet, "/api/workflows", "flx_nope", http.StatusUnauthorized},
		{"revoked key", http.MethodGet, "/api/workflows", revoked, http.StatusUnauthorized},
		{"no key on ws", http.MethodGet, "/ws", "", http.StatusUnauthorized},
		{"no key on unknown route", http.MethodGet, "/api/nope", "", http.StatusUnauthorized},
		{"viewer reads", http.MethodGet, "/api/workflows", viewer, http.StatusOK},
		{"viewer on unknown route", http.MethodGet, "/api/nope", viewer, http.StatusNotFound},
		{"viewer triggers", http.MethodPost, trigger, viewer, http.StatusForbidden},
		{"operator triggers", http.MethodPost, trigger, operator, http.StatusAccepted},
		{"health is public", http.MethodGet, "/api/health", "", http.StatusOK},
	} {
		rr := call(tc.method, tc.path, tc.key)
		if rr.Code != tc.want {
			t.Errorf("%s: %s %s = %d, want %d: %s", tc.name, tc.method, tc.path, rr.Code, tc.want, rr.Body)
			continue
		}
		switch tc.want {
		case http.StatusUnauthorized:
			if got := rr.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("%s: WWW-Authenticate = %q, want Bearer", tc.name, got)
			}
			if !strings.Contains(rr.Body.String(), `"authentication required"`) {
				t.Errorf("%s: body = %s", tc.name, rr.Body)
			}
		case http.StatusForbidden:
			if !strings.Contains(rr.Body.String(), `"insufficient role"`) {
				t.Errorf("%s: body = %s", tc.name, rr.Body)
			}
		}
	}

	// Key management is admin-only, and a key it issues works at once.
	if rr := call(http.MethodGet, "/api/keys", operator); rr.Code != http.StatusForbidden {
		t.Errorf("operator GET /api/keys = %d, want 403", rr.Code)
	}
	admin := key("admin", "admin")
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/keys", strings.NewReader(`{"name":"new","role":"viewer"}`))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+admin)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	var created struct {
		Key  string `json:"key"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || rr.Code != http.StatusCreated || created.Role != "viewer" {
		t.Fatalf("admin POST /api/keys = %d %s (%v)", rr.Code, rr.Body, err)
	}
	if rr := call(http.MethodGet, "/api/workflows", created.Key); rr.Code != http.StatusOK {
		t.Errorf("issued key: GET /api/workflows = %d, want 200", rr.Code)
	}
	if rr := call(http.MethodGet, "/api/keys", admin); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "hash") {
		t.Errorf("admin GET /api/keys = %d %s, want 200 without hashes", rr.Code, rr.Body)
	}
}

func TestAPI_AuthLogsKeyNotSecret(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	srv := api.NewHandler(store, redis, nil, api.NewHub(), nil).Server(nil)
	plain, k, err := store.CreateAPIKey(ctx, "logged", "viewer")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/workflows", nil)
	req.Host = "localhost"
	req.Header.Set("Authorization", "Bearer "+plain)
	srv.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	if !strings.Contains(out, `"api_key_id":"`+k.ID+`"`) || !strings.Contains(out, `"key_name":"logged"`) {
		t.Errorf("access log lacks the key id/name:\n%s", out)
	}
	if strings.Contains(out, plain) {
		t.Errorf("access log contains the plaintext key:\n%s", out)
	}
}
