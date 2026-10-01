package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// ListWorkflowRevisions lists a workflow's saved revisions, newest first.
// GET /api/workflows/{id}/revisions
func (h *Handler) ListWorkflowRevisions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit, offset, err := parsePagination(r, 50)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	// An unknown workflow is a 404, not an empty history.
	if _, err := h.store.GetWorkflowDefinition(r.Context(), id); err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "workflow not found", nil)
		} else {
			writeError(w, r, http.StatusInternalServerError, "internal server error", err)
		}
		return
	}
	revs, err := h.store.ListWorkflowRevisions(r.Context(), id, limit, offset)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list revisions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": revs, "count": len(revs)})
}

// GetWorkflowRevision returns the definition as saved at one revision.
// Restoring it is a PUT of that body to /api/workflows/{id}.
// GET /api/workflows/{id}/revisions/{rev}
func (h *Handler) GetWorkflowRevision(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil || rev < 1 {
		writeError(w, r, http.StatusBadRequest, "revision must be a positive integer", nil)
		return
	}
	def, err := h.store.GetWorkflowRevision(r.Context(), r.PathValue("id"), rev)
	if err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "revision not found", nil)
		} else {
			writeError(w, r, http.StatusInternalServerError, "internal server error", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, def)
}
