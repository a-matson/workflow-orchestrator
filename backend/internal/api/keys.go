package api

import (
	"encoding/json"
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid request body", err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || !ValidRole(req.Role) {
		writeError(w, r, http.StatusBadRequest, "name is required and role must be admin, operator or viewer", nil)
		return
	}
	plaintext, key, err := h.store.CreateAPIKey(r.Context(), req.Name, req.Role)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to create API key", err)
		return
	}
	logFrom(r).Info().Str("new_key_id", key.ID).Str("new_key_role", key.Role).Msg("API key created")
	writeJSON(w, http.StatusCreated, struct {
		*persistence.APIKey
		Key string `json:"key"`
	}{key, plaintext})
}

// RevokeAPIKey revokes a key immediately; revocation cannot be undone.
func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	err := h.store.RevokeAPIKey(r.Context(), r.PathValue("id"))
	if errors.Is(err, persistence.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "API key not found", nil)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to revoke API key", err)
		return
	}
	logFrom(r).Info().Str("revoked_key_id", r.PathValue("id")).Msg("API key revoked")
	w.WriteHeader(http.StatusNoContent)
}
