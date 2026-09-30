package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The frontend does JSON.parse per frame, so a frame holding several
// newline-joined events is dropped whole.
func TestWritePump_OneJSONPerFrame(t *testing.T) {
	hub := NewHub()
	client := &Client{send: make(chan []byte, 4), hub: hub, filters: map[string]bool{}}
	// Queued before writePump starts so both are available to be batched.
	client.send <- []byte(`{"type":"a"}`)
	client.send <- []byte(`{"type":"b"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := hub.upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		client.conn = conn
		client.writePump(r.Context())
	}))
	defer srv.Close()
	defer close(client.send) // lets writePump return instead of leaking past the test

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	defer func() { _ = conn.Close() }() // test teardown; nothing to act on
	// A deadline error would surface as a ReadMessage failure below.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(first, &v); err != nil {
		t.Fatalf("frame 1 is not a single JSON value: %q: %v", first, err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("expected a second frame: %v", err)
	}
}

// dialAs connects to a hub whose upgrade request carries p, as the auth
// middleware would leave it.
func dialAs(t *testing.T, hub *Hub, p *Principal) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}))
	t.Cleanup(srv.Close)
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()                  // handshake response has no body worth reading
	t.Cleanup(func() { _ = conn.Close() }) // test teardown; nothing to act on
	return conn
}

func TestWS_RevokedPrincipalIsDisconnected(t *testing.T) {
	var revoked atomic.Bool
	check := func(_ context.Context, p *Principal) (bool, error) { return p.KeyID == "k1" && !revoked.Load(), nil }
	hub := NewHub(WithPrincipalRecheck(check, 20*time.Millisecond))
	go hub.Run()
	conn := dialAs(t, hub, &Principal{KeyID: "k1", Role: RoleViewer})

	// Several recheck intervals pass while the key is valid.
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)) // expiry is the expected outcome
	if _, _, err := conn.ReadMessage(); websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("valid key was disconnected: %v", err)
	}

	conn = dialAs(t, hub, &Principal{KeyID: "k1", Role: RoleViewer})
	revoked.Store(true)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second)) // a timeout fails the check below
	_, _, err := conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("after revocation: %v, want close 1008", err)
	}
}

func TestWS_RecheckTransientErrorKeepsConnection(t *testing.T) {
	check := func(context.Context, *Principal) (bool, error) { return false, errors.New("db down") }
	hub := NewHub(WithPrincipalRecheck(check, 20*time.Millisecond))
	go hub.Run()
	conn := dialAs(t, hub, &Principal{KeyID: "k1"})
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)) // expiry is the expected outcome
	if _, _, err := conn.ReadMessage(); websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("check error disconnected the client: %v", err)
	}
}
