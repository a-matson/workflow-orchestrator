package api

import (
	"net/http"
	"time"
)

// Stubs so the session tests fail on their assertions; the real browser
// session lands in the next commit.

func (h *Handler) CreateSession(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func (h *Handler) GetSession(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func (h *Handler) DeleteSession(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func signSession(_ []byte, _ string, _ time.Time) string { return "" }

func verifySession(_ []byte, _ string, _ time.Time) (string, error) { return "", nil }
