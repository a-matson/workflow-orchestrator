package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	webhookTimestampHeader = "X-Fluxor-Timestamp"
	webhookSignatureHeader = "X-Fluxor-Signature"
	// webhookWindow bounds clock skew and how long a captured request stays
	// usable; within it, replays are refused by signature.
	webhookWindow = 5 * time.Minute
)

// TriggerWebhook starts a run of a workflow from a request signed with the
// secret its definition's webhook names. The JSON body is the run's payload.
// POST /api/hooks/{id}
func (h *Handler) TriggerWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	body, err := io.ReadAll(r.Body)
	if isTooBig(err) {
		writeError(w, r, http.StatusRequestEntityTooLarge, errBodyTooLarge, nil)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "could not read body", err)
		return
	}

	// Every rejection answers alike, so a caller cannot tell an unknown
	// workflow, one without a webhook and a bad signature apart.
	deny := func(reason string, err error) {
		logFrom(r).Warn().Err(err).Str("workflow_id", id).Str("reason", reason).Msg("webhook rejected")
		h.audit(r, "workflow.webhook", "workflow", id, auditDenied)
		writeError(w, r, http.StatusUnauthorized, "invalid webhook signature", nil)
	}
	def, err := h.store.GetWorkflowDefinition(ctx, id)
	if err != nil || def.Webhook == nil {
		deny("no webhook for this workflow", err)
		return
	}
	key, err := h.secrets.Secret(ctx, def.Webhook.Secret)
	if err != nil {
		deny("webhook secret unavailable", err)
		return
	}
	ts, sig := r.Header.Get(webhookTimestampHeader), r.Header.Get(webhookSignatureHeader)
	if !validWebhookSignature(key, ts, sig, body, time.Now()) {
		deny("bad or stale signature", nil)
		return
	}
	// Twice the window: a request is accepted up to webhookWindow on either side of its timestamp.
	fresh, err := h.redis.ClaimOnce(ctx, "fluxor:webhook:"+id+":"+sig, 2*webhookWindow)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "could not check for a replay", err)
		return
	}
	if !fresh {
		deny("replayed request", nil)
		return
	}

	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			writeError(w, r, http.StatusBadRequest, "body must be a JSON object", nil)
			return
		}
	}
	exec, err := h.orchestrator.StartWorkflow(ctx, def, payload)
	if err != nil {
		h.audit(r, "workflow.webhook", "workflow", id, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to start workflow", err)
		return
	}
	h.audit(r, "workflow.webhook", "workflow", id, auditSuccess)
	logFrom(r).Info().Str("workflow_exec_id", exec.ID).Str("workflow_id", id).Msg("workflow execution started by webhook")
	// Only the ID: the sender holds a signing key, not an API key to read runs.
	writeJSON(w, http.StatusAccepted, map[string]string{"execution_id": exec.ID})
}

// validWebhookSignature reports whether sig is "sha256=" plus the hex
// HMAC-SHA256, under key, of ts + "." + body, and ts (Unix seconds) is within
// webhookWindow of now.
func validWebhookSignature(key, ts, sig string, body []byte, now time.Time) bool {
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if skew := now.Sub(time.Unix(sec, 0)); skew > webhookWindow || skew < -webhookWindow {
		return false
	}
	hexMAC, ok := strings.CutPrefix(sig, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexMAC)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
