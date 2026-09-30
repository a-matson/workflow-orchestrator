//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func TestSession_CookieFlags(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	srv := api.NewHandler(store, redis, nil, api.NewHub(), nil).Server(nil)

	plain, key, err := store.CreateAPIKey(ctx, "browser", "operator")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	revoked, rk, err := store.CreateAPIKey(ctx, "gone", "admin")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := store.RevokeAPIKey(ctx, rk.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	call := func(method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		if c != nil {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	sessionCookie := func(rr *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		for _, c := range rr.Result().Cookies() {
			if c.Name == "fluxor_session" {
				return c
			}
		}
		t.Fatalf("no fluxor_session cookie in %v", rr.Header().Values("Set-Cookie"))
		return nil
	}

	// A bad key gets the same answer whether it is unknown or revoked.
	unknown := call(http.MethodPost, "/api/session", `{"api_key":"flx_nope"}`, nil)
	gone := call(http.MethodPost, "/api/session", `{"api_key":"`+revoked+`"}`, nil)
	if unknown.Code != http.StatusUnauthorized || gone.Code != http.StatusUnauthorized ||
		unknown.Body.String() != gone.Body.String() {
		t.Errorf("bad keys: unknown = %d %s, revoked = %d %s; want identical 401s",
			unknown.Code, unknown.Body, gone.Code, gone.Body)
	}
	if len(gone.Result().Cookies()) != 0 {
		t.Errorf("revoked key got a cookie: %v", gone.Header().Values("Set-Cookie"))
	}

	rr := call(http.MethodPost, "/api/session", `{"api_key":"`+plain+`"}`, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/session = %d, want 200: %s", rr.Code, rr.Body)
	}
	var who struct{ Name, Role string }
	if err := json.Unmarshal(rr.Body.Bytes(), &who); err != nil || who.Name != "browser" || who.Role != "operator" {
		t.Errorf("POST /api/session body = %s (%v), want name browser, role operator", rr.Body, err)
	}
	if strings.Contains(rr.Body.String(), plain) {
		t.Errorf("response echoes the key: %s", rr.Body)
	}
	c := sessionCookie(rr)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 43200 || c.Secure {
		t.Errorf("cookie = %+v; want HttpOnly, SameSite=Strict, Path=/, Max-Age=43200, not Secure over plain HTTP", c)
	}
	if strings.Contains(c.Value, plain) || strings.Contains(c.Value, key.ID) {
		t.Errorf("cookie value carries the key or its id in the clear: %q", c.Value)
	}
	session := &http.Cookie{Name: c.Name, Value: c.Value}

	if rr := call(http.MethodGet, "/api/workflows", "", session); rr.Code != http.StatusOK {
		t.Errorf("GET /api/workflows with the cookie = %d, want 200: %s", rr.Code, rr.Body)
	}
	if rr := call(http.MethodGet, "/api/session", "", session); rr.Code != http.StatusOK ||
		!strings.Contains(rr.Body.String(), `"name":"browser"`) || !strings.Contains(rr.Body.String(), `"role":"operator"`) {
		t.Errorf("GET /api/session = %d %s, want 200 with name and role", rr.Code, rr.Body)
	}
	if rr := call(http.MethodGet, "/ws", "", session); rr.Code == http.StatusUnauthorized {
		t.Errorf("GET /ws with the cookie = 401, want the upgrade to pass auth")
	}
	if rr := call(http.MethodGet, "/api/session", "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/session without a cookie = %d, want 401", rr.Code)
	}

	tampered := &http.Cookie{Name: c.Name, Value: c.Value[:len(c.Value)-2] + "AA"}
	if tampered.Value == c.Value {
		tampered.Value = c.Value[:len(c.Value)-2] + "BB"
	}
	if rr := call(http.MethodGet, "/api/workflows", "", tampered); rr.Code != http.StatusUnauthorized {
		t.Errorf("tampered cookie: GET /api/workflows = %d, want 401", rr.Code)
	}

	rr = call(http.MethodDelete, "/api/session", "", session)
	if rr.Code != http.StatusNoContent {
		t.Errorf("DELETE /api/session = %d, want 204: %s", rr.Code, rr.Body)
	} else if cleared := sessionCookie(rr); cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("DELETE /api/session cookie = %+v, want an empty value with Max-Age=0", cleared)
	}

	// Revocation ends a live session at once, not when the cookie expires.
	if err := store.RevokeAPIKey(ctx, key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if rr := call(http.MethodGet, "/api/workflows", "", session); rr.Code != http.StatusUnauthorized {
		t.Errorf("after revocation: GET /api/workflows = %d, want 401", rr.Code)
	}
}

// A cookie session's principal must carry its key id, or the /ws re-check
// could not notice that the key behind a browser tab was revoked.
func TestSession_RevokedKeyClosesWebSocket(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	hub := api.NewHub(api.WithPrincipalRecheck(api.NewAuthenticator(store).StillValid, 50*time.Millisecond))
	go hub.Run()
	srv := httptest.NewServer(api.NewHandler(store, redis, nil, hub, nil).Server(nil))
	defer srv.Close()

	plain, k, err := store.CreateAPIKey(ctx, "tab", "viewer")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/session",
		strings.NewReader(`{"api_key":"`+plain+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/session: %v", err)
	}
	_ = resp.Body.Close() // only the status and cookie matter
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == "fluxor_session" {
			cookie = c.Name + "=" + c.Value
		}
	}
	if resp.StatusCode != http.StatusOK || cookie == "" {
		t.Fatalf("POST /api/session = %d, cookie %q; want 200 with fluxor_session", resp.StatusCode, cookie)
	}

	conn, wsResp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws",
		http.Header{"Cookie": {cookie}})
	if err != nil {
		t.Fatalf("dial with the session cookie: %v", err)
	}
	_ = wsResp.Body.Close()             // handshake response has no body worth reading
	defer func() { _ = conn.Close() }() // test teardown; nothing to act on

	if err := store.RevokeAPIKey(ctx, k.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second)) // a timeout fails the check below
	if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("after revocation: %v, want close 1008", err)
	}
}
