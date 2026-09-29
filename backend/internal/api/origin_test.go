package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

const evil = "http://evil.example"

// run sends a request through OriginPolicy and reports whether the wrapped handler ran.
func run(t *testing.T, allowed []string, method, origin, ctype string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	ran := false
	h := OriginPolicy(allowed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequestWithContext(context.Background(), method, "http://localhost:8080/api/workflows", strings.NewReader("{}"))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, ran
}

func TestCORS_ForeignOrigin(t *testing.T) {
	rec, _ := run(t, nil, http.MethodGet, evil, "")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO = %q, want none", got)
	}
}

func TestAPI_ForeignOriginSimplePost(t *testing.T) {
	rec, ran := run(t, nil, http.MethodPost, evil, "text/plain")
	if rec.Code != http.StatusForbidden || ran {
		t.Fatalf("code=%d ran=%v, want 403 and handler not run", rec.Code, ran)
	}
}

func TestAPI_ForeignOriginPreflight(t *testing.T) {
	rec, _ := run(t, nil, http.MethodOptions, evil, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, want 403", rec.Code)
	}
}

func TestAPI_AllowedOrigin(t *testing.T) {
	const ok = "http://localhost:5173"
	rec, _ := run(t, []string{" ", ok}, http.MethodGet, ok, "")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != ok {
		t.Fatalf("ACAO = %q, want %q", got, ok)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatalf("Vary = %q, want Origin", rec.Header().Get("Vary"))
	}
	// Same-origin: Origin host equals request Host, no allowlist entry needed.
	_, ran := run(t, nil, http.MethodPost, "http://localhost:8080", "application/json")
	if !ran {
		t.Fatal("same-origin JSON POST did not reach handler")
	}
}

func TestAPI_MutationRequiresJSON(t *testing.T) {
	rec, ran := run(t, nil, http.MethodPost, "", "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType || ran {
		t.Fatalf("code=%d ran=%v, want 415 and handler not run", rec.Code, ran)
	}
	if _, ran := run(t, nil, http.MethodPost, "", "application/json; charset=utf-8"); !ran {
		t.Fatal("JSON POST with charset did not reach handler")
	}
}

func TestAPI_DNSRebindingRejected(t *testing.T) {
	ran := false
	h := OriginPolicy(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://rebind.evil:8080/api/workflows", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://rebind.evil:8080")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest || ran {
		t.Fatalf("code=%d ran=%v, want 421 and handler not run", rec.Code, ran)
	}
}

func TestAPI_UntrustedHostRejected(t *testing.T) {
	ran := false
	h := OriginPolicy(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://rebind.evil:8080/api/workflows", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest || ran {
		t.Fatalf("code=%d ran=%v, want 421 and handler not run", rec.Code, ran)
	}
}

func TestAPI_AllowedPreflight(t *testing.T) {
	const ok = "http://localhost:5173"
	rec, _ := run(t, []string{ok}, http.MethodOptions, ok, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d, want 204", rec.Code)
	}
}

func TestAPI_ForeignGetReachesHandler(t *testing.T) {
	rec, ran := run(t, nil, http.MethodGet, evil, "")
	if !ran || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("ran=%v acao=%q, want handler run without ACAO", ran, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestAPI_DeleteRequiresJSON(t *testing.T) {
	rec, ran := run(t, nil, http.MethodDelete, "", "")
	if rec.Code != http.StatusUnsupportedMediaType || ran {
		t.Fatalf("code=%d ran=%v, want 415 and handler not run", rec.Code, ran)
	}
}

func TestWS_AllowedOrigin(t *testing.T) {
	hub := NewHub(WithAllowedOrigins([]string{"http://localhost:5173"}))
	go hub.Run()
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"http://localhost:5173"}})
	if err != nil {
		t.Fatalf("allowlisted origin handshake failed: %v", err)
	}
	_ = resp.Body.Close()
	_ = conn.Close()
}

func TestWS_ForeignOrigin(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	_, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {evil}})
	if err == nil {
		t.Fatal("foreign origin handshake succeeded")
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resp = %v, want 403", resp)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("no-origin handshake failed: %v", err)
	}
	_ = resp.Body.Close()
	_ = conn.Close()
}
