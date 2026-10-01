package api

import (
	"context"
	"net/http"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

const (
	auditSuccess = "success"
	auditDenied  = "denied"
	auditError   = "error"
)

// auditActions names every mutating route's action. The auth middleware uses
// it to record denied attempts; handlers use the same names for their own.
var auditActions = map[string]string{
	"POST /api/workflows":              "workflow.create",
	"POST /api/workflows/import":       "workflow.import",
	"PUT /api/workflows/{id}":          "workflow.update",
	"POST /api/workflows/{id}/trigger": "workflow.trigger",
	"POST /api/executions/{id}/cancel": "execution.cancel",
	"POST /api/executions/{id}/retry":  "execution.retry",
	"POST /api/executions/{id}/resume": "execution.resume",
	"POST /api/keys":                   "key.create",
	"DELETE /api/keys/{id}":            "key.revoke",
	"PUT /api/secrets/{name}":          "secret.set",
	"DELETE /api/secrets/{name}":       "secret.delete",
	"POST /api/session":                "session.login",
	"DELETE /api/session":              "session.logout",
}

// recordAudit writes one audit row for r. p is the actor; nil means the
// request's own principal, and failing that an anonymous one.
//
// A failed write is logged, never returned: availability over completeness,
// so a database hiccup cannot turn a finished mutation into an error. The
// error log line still names the actor and action.
func recordAudit(r *http.Request, store *persistence.Store, p *Principal, action, targetType, targetID, outcome string) {
	if p == nil {
		p = PrincipalFrom(r.Context())
	}
	reqID, _ := r.Context().Value(RequestIDKey).(string)
	e := persistence.AuditEntry{ActorName: "anonymous", Action: action, TargetType: targetType,
		TargetID: targetID, RequestID: reqID, Outcome: outcome}
	if p != nil {
		e.ActorName = p.Name
		e.ActorKeyID = &p.KeyID
	}
	// The request context may already be cancelled (client gone); the
	// mutation happened regardless, so the record must not depend on it.
	if err := store.RecordAudit(context.WithoutCancel(r.Context()), e); err != nil {
		logFrom(r).Error().Err(err).Str("action", action).Str("target_id", targetID).
			Str("outcome", outcome).Str("actor", e.ActorName).Msg("audit write failed")
	}
}

func (h *Handler) audit(r *http.Request, action, targetType, targetID, outcome string) {
	recordAudit(r, h.store, nil, action, targetType, targetID, outcome)
}

// ListAudit returns the audit trail, newest first.
// GET /api/audit?limit&offset
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parsePagination(r, 50)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	entries, err := h.store.ListAudit(r.Context(), limit, offset)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list audit log", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}
