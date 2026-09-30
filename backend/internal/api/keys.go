package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// ListAPIKeys returns every key's metadata, revoked keys included.
func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list API keys", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "count": len(keys)})
}

// CreateAPIKey issues a key; the response is the only time its plaintext is shown.
func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	// The name is logged on every request as key_name, so keep it short.
	if req.Name == "" || len(req.Name) > 100 || !ValidRole(req.Role) {
		writeError(w, r, http.StatusBadRequest, "name must be 1-100 bytes and role admin, operator or viewer", nil)
		return
	}
	plaintext, key, err := h.store.CreateAPIKey(r.Context(), req.Name, req.Role)
	if err != nil {
		h.audit(r, "key.create", "api_key", "", auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to create API key", err)
		return
	}
	h.audit(r, "key.create", "api_key", key.ID, auditSuccess)
	logFrom(r).Info().Str("new_key_id", key.ID).Str("new_key_role", key.Role).Msg("API key created")
	writeJSON(w, http.StatusCreated, struct {
		*persistence.APIKey
		Key string `json:"key"`
	}{key, plaintext})
}

// RevokeAPIKey revokes a key; it cannot be undone. HTTP requests see it at
// once, open WebSockets within the hub's recheck interval (30s in main).
func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	err := h.store.RevokeAPIKey(r.Context(), r.PathValue("id"))
	if errors.Is(err, persistence.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "API key not found", nil)
		return
	}
	if err != nil {
		h.audit(r, "key.revoke", "api_key", r.PathValue("id"), auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to revoke API key", err)
		return
	}
	h.audit(r, "key.revoke", "api_key", r.PathValue("id"), auditSuccess)
	logFrom(r).Info().Str("revoked_key_id", r.PathValue("id")).Msg("API key revoked")
	w.WriteHeader(http.StatusNoContent)
}
