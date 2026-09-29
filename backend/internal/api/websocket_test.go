package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		client.writePump()
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
