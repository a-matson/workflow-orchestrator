package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// serveLogged sends one request through the production middleware order and
// returns the response plus every JSON log line it produced.
func serveLogged(t *testing.T, inboundID string) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })

	// A nil store is safe: invalid pagination is rejected before it is used,
	// and that rejection logs through writeError.
	routes := NewHandler(nil, nil, nil, nil, nil).Routes()
	h := ChainMiddleware(routes, RequestIDMiddleware, RecoveryMiddleware, LoggingMiddleware)

	r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/workflows?limit=-1", nil)
	if inboundID != "" {
		r.Header.Set("X-Request-ID", inboundID)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", raw, err)
		}
		lines = append(lines, m)
	}
	return w, lines
}

func TestRequestLogs_CarryRequestID(t *testing.T) {
	w, lines := serveLogged(t, "")
	id := w.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("response has no X-Request-ID")
	}
	if len(lines) < 2 {
		t.Fatalf("want handler error line and access line, got %d: %v", len(lines), lines)
	}
	for _, l := range lines {
		if l["request_id"] != id {
			t.Errorf("line %v: request_id = %v, want %s", l["message"], l["request_id"], id)
		}
		if l["method"] != "GET" || l["path"] != "/api/workflows" {
			t.Errorf("line %v: method/path = %v %v", l["message"], l["method"], l["path"])
		}
	}
}

func TestRequestID_InboundIsValidated(t *testing.T) {
	// A well-formed client id is kept so callers can correlate their own traces.
	w, lines := serveLogged(t, "client-trace_1.2")
	if got := w.Header().Get("X-Request-ID"); got != "client-trace_1.2" {
		t.Errorf("valid inbound id replaced by %q", got)
	}
	if lines[0]["request_id"] != "client-trace_1.2" {
		t.Errorf("log request_id = %v", lines[0]["request_id"])
	}

	// Quotes, spaces and oversize values could forge log fields or bloat logs.
	for _, bad := range []string{`x","admin":true,"y":"`, "has space", strings.Repeat("a", 65)} {
		w, lines := serveLogged(t, bad)
		got := w.Header().Get("X-Request-ID")
		if got == bad || got == "" {
			t.Errorf("untrusted id %q was accepted (got %q)", bad, got)
		}
		for _, l := range lines {
			if l["request_id"] != got || l["admin"] != nil {
				t.Errorf("untrusted id leaked into log: %v", l)
			}
		}
	}
}
