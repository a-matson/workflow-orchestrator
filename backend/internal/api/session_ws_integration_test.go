//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// Internal to the package so the test can mint a cookie that lapses in
// milliseconds instead of waiting out the 12h TTL.
func TestSession_ExpiredCookieClosesWebSocket(t *testing.T) {
	store, redis := testutil.Env(t)
	ctx := context.Background()
	hub := NewHub(WithPrincipalRecheck(NewAuthenticator(store).StillValid, 50*time.Millisecond))
	go hub.Run()
	h := NewHandler(store, redis, nil, hub, nil)
	srv := httptest.NewServer(h.Server(nil))
	defer srv.Close()

	_, k, err := store.CreateAPIKey(ctx, "short", "viewer")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	cookie := sessionCookie + "=" + signSession(h.session.Secret, k.ID, time.Now().Add(1500*time.Millisecond))
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws",
		http.Header{"Cookie": {cookie}})
	if err != nil {
		t.Fatalf("dial with a live cookie: %v", err)
	}
	_ = resp.Body.Close()               // handshake response has no body worth reading
	defer func() { _ = conn.Close() }() // test teardown; nothing to act on

	// The key stays active throughout, so only the expiry can close the stream.
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second)) // a timeout fails the check below
	opened := time.Now()
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("after expiry: %v, want close 1008", err)
	}
	if time.Since(opened) < 500*time.Millisecond {
		t.Errorf("closed after %v, before the cookie expired", time.Since(opened))
	}
}
