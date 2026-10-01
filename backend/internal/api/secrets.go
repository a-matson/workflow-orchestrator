package api

import (
	"errors"
	"net/http"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/secrets"
)

// WithSecrets enables the secrets endpoints; without it they answer 503.
func (h *Handler) WithSecrets(v *secrets.Vault) *Handler {
	h.secrets = v
	return h
}

// ListSecrets returns secret names only: values never leave the server.
// GET /api/secrets
func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	names, err := h.secrets.Names(r.Context())
	if errors.Is(err, secrets.ErrNotConfigured) {
		writeError(w, r, http.StatusServiceUnavailable, err.Error(), nil)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list secrets", err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": names})
}

// PutSecret creates or replaces a secret from {"value": "..."}. The response
// does not echo the value. PUT /api/secrets/{name}
func (h *Handler) PutSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !secrets.ValidName(name) {
		writeError(w, r, http.StatusBadRequest, "secret name must match [A-Za-z0-9_.-], 1-64 characters", nil)
		return
	}
	err := h.secrets.Set(r.Context(), name, body.Value)
	switch {
	case errors.Is(err, secrets.ErrNotConfigured):
		writeError(w, r, http.StatusServiceUnavailable, err.Error(), nil)
		return
	case err != nil && len(body.Value) > secrets.MaxValueSize:
		writeError(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case err != nil:
		h.audit(r, "secret.set", "secret", name, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to store secret", err)
		return
	}
	h.audit(r, "secret.set", "secret", name, auditSuccess)
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// DeleteSecret removes a secret. DELETE /api/secrets/{name}
func (h *Handler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := h.secrets.Delete(r.Context(), name)
	switch {
	case errors.Is(err, secrets.ErrNotConfigured):
		writeError(w, r, http.StatusServiceUnavailable, err.Error(), nil)
		return
	case errors.Is(err, persistence.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "secret not found", nil)
		return
	case err != nil:
		h.audit(r, "secret.delete", "secret", name, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to delete secret", err)
		return
	}
	h.audit(r, "secret.delete", "secret", name, auditSuccess)
	w.WriteHeader(http.StatusNoContent)
}
