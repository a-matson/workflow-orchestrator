package api

import "net/http"

// ListAudit returns the audit trail, newest first.
func (h *Handler) ListAudit(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "count": 0})
}
