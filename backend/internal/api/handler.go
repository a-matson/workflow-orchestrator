package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/storage"
)

// Handler provides HTTP API endpoints for the workflow platform
type Handler struct {
	store        *persistence.Store
	redis        *persistence.RedisClient
	orchestrator *orchestrator.Orchestrator
	hub          *Hub
	storage      *storage.Client
	session      SessionConfig
	trusted      []netip.Prefix
	maxBody      int64 // bytes; see MaxBody
	generalLimit int   // requests per minute per client IP
	loginLimit   int   // POST /api/session per minute per client IP
}

// NewHandler returns a Handler whose session secret is random, so sessions
// last only as long as the process; production sets one with WithSession.
func NewHandler(store *persistence.Store, redis *persistence.RedisClient, orch *orchestrator.Orchestrator, hub *Hub, sc *storage.Client) *Handler {
	return &Handler{store: store, redis: redis, orchestrator: orch, hub: hub, storage: sc,
		session: SessionConfig{Secret: RandomSessionSecret()},
		maxBody: DefaultMaxBody, generalLimit: 200, loginLimit: 10}
}

// WithSession replaces the session cookie configuration.
func (h *Handler) WithSession(cfg SessionConfig) *Handler {
	h.session = cfg
	return h
}

// WithTrustedProxies sets the reverse proxies whose X-Forwarded-For the rate
// limiter believes.
func (h *Handler) WithTrustedProxies(p []netip.Prefix) *Handler {
	h.trusted = p
	return h
}

// WithRateLimits sets the per-minute budgets of the general API and of login.
func (h *Handler) WithRateLimits(general, login int) *Handler {
	h.generalLimit, h.loginLimit = general, login
	return h
}

// WithMaxBody sets the largest request body, in bytes, the API reads.
func (h *Handler) WithMaxBody(n int64) *Handler {
	h.maxBody = n
	return h
}

// Routes registers every endpoint on a fresh mux, without the middleware chain.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern, fn := range h.routes() {
		mux.HandleFunc(pattern, fn)
	}
	return mux
}

// routes is the single list of endpoints, so the auth policy test can check
// every pattern against routePolicy.
func (h *Handler) routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		// Workflow definitions
		"POST /api/workflows":     h.CreateWorkflow,
		"GET /api/workflows":      h.ListWorkflows,
		"GET /api/workflows/{id}": h.GetWorkflow,
		"PUT /api/workflows/{id}": h.UpdateWorkflow,

		// Workflow executions
		"POST /api/workflows/{id}/trigger": h.TriggerWorkflow,
		"GET /api/executions":              h.ListExecutions,
		"GET /api/executions/{id}":         h.GetExecution,
		"POST /api/executions/{id}/cancel": h.CancelExecution,
		"POST /api/executions/{id}/retry":  h.RetryExecution,

		// Task executions
		"GET /api/executions/{execID}/tasks": h.ListTasks,
		"GET /api/tasks/{id}":                h.GetTask,
		"GET /api/tasks/{id}/logs":           h.GetTaskLogs,

		// System
		"GET /api/metrics": h.GetMetrics,
		"GET /api/health":  h.Health,
		"GET /api/ready":   h.Ready,

		// Artifacts
		"GET /api/tasks/{id}/artifacts": h.ListTaskArtifacts,
		"GET /api/artifacts/url":        h.GetArtifactURL,

		// API keys
		"GET /api/keys":         h.ListAPIKeys,
		"POST /api/keys":        h.CreateAPIKey,
		"DELETE /api/keys/{id}": h.RevokeAPIKey,

		// Browser session
		"POST /api/session":   h.CreateSession,
		"GET /api/session":    h.GetSession,
		"DELETE /api/session": h.DeleteSession,

		"GET /ws": func(w http.ResponseWriter, r *http.Request) { h.hub.ServeWS(w, r) },
	}
}

// ==================== Workflow Definitions ====================

func (h *Handler) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var def models.WorkflowDefinition
	if !decodeJSON(w, r, &def) {
		return
	}
	if err := validateDefinition(&def); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}

	if def.ID == "" {
		def.ID = uuid.New().String()
	}
	now := time.Now()
	def.CreatedAt = now
	def.UpdatedAt = now

	if err := h.store.SaveWorkflowDefinition(r.Context(), &def); err != nil {
		logFrom(r).Error().Err(err).Msg("failed to save workflow definition")
		writeError(w, r, http.StatusInternalServerError, "failed to save workflow", err)
		return
	}

	writeJSON(w, http.StatusCreated, def)
}

func (h *Handler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parsePagination(r, 50)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	defs, err := h.store.ListWorkflowDefinitions(r.Context(), limit, offset)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list workflows", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": defs, "count": len(defs)})
}

// UpdateWorkflow upserts a workflow definition by ID
// PUT /api/workflows/{id}
func (h *Handler) UpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var def models.WorkflowDefinition
	if !decodeJSON(w, r, &def) {
		return
	}
	if err := validateDefinition(&def); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	def.ID = id // ensure URL ID wins
	now := time.Now()
	if def.CreatedAt.IsZero() {
		def.CreatedAt = now
	}
	def.UpdatedAt = now
	if err := h.store.SaveWorkflowDefinition(r.Context(), &def); err != nil {
		logFrom(r).Error().Err(err).Str("id", id).Msg("failed to update workflow definition")
		writeError(w, r, http.StatusInternalServerError, "failed to update workflow", err)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	def, err := h.store.GetWorkflowDefinition(r.Context(), id)
	if err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "workflow not found", nil)
		} else {
			writeError(w, r, http.StatusInternalServerError, "internal server error", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// ==================== Executions ====================

func (h *Handler) TriggerWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	def, err := h.store.GetWorkflowDefinition(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "workflow not found", err)
		return
	}

	var payload map[string]any
	// An absent or malformed payload means "no inputs"; only an oversized one is refused.
	if err := json.NewDecoder(r.Body).Decode(&payload); isTooBig(err) {
		writeError(w, r, http.StatusRequestEntityTooLarge, errBodyTooLarge, nil)
		return
	}

	exec, err := h.orchestrator.StartWorkflow(r.Context(), def, payload)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to start workflow", err)
		return
	}

	logFrom(r).Info().
		Str("workflow_exec_id", exec.ID).
		Str("workflow_id", def.ID).
		Msg("workflow execution started")
	writeJSON(w, http.StatusAccepted, exec)
}

func (h *Handler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parsePagination(r, 50)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}

	execs, err := h.store.ListWorkflowExecutions(r.Context(), limit, offset)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list executions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"executions": execs, "count": len(execs)})
}

func (h *Handler) GetExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exec, err := h.store.GetWorkflowExecution(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "execution not found", err)
		return
	}
	writeJSON(w, http.StatusOK, exec)
}

func (h *Handler) CancelExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exec, err := h.store.GetWorkflowExecution(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "execution not found", err)
		return
	}
	if exec.Status != models.WorkflowStatusRunning && exec.Status != models.WorkflowStatusPending {
		writeError(w, r, http.StatusConflict, "execution is not cancellable", err)
		return
	}
	now := time.Now()
	exec.Status = models.WorkflowStatusCancelled
	exec.CompletedAt = &now
	exec.UpdatedAt = now
	if err := h.store.UpdateWorkflowExecution(r.Context(), exec); err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to cancel execution", err)
		return
	}
	h.hub.Broadcast(models.WebSocketEvent{Type: models.WSEventWorkflowFailed, Payload: exec})
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled", "id": id})
}

func (h *Handler) RetryExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exec, err := h.store.GetWorkflowExecution(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "execution not found", err)
		return
	}

	def, err := h.store.GetWorkflowDefinition(r.Context(), exec.WorkflowID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "workflow definition not found", err)
		return
	}

	newExec, err := h.orchestrator.StartWorkflow(r.Context(), def, exec.TriggerPayload)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to retry: "+err.Error(), err)
		return
	}

	writeJSON(w, http.StatusAccepted, newExec)
}

// ==================== Tasks ====================

func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	execID := r.PathValue("execID")
	tasks, err := h.store.ListTaskExecutions(r.Context(), execID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list tasks", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "count": len(tasks)})
}

func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := h.store.GetTaskExecution(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "task not found", err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *Handler) GetTaskLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := h.store.GetTaskExecution(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "task not found", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": task.Logs, "task_id": id})
}

// ==================== System ====================

func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := h.orchestrator.GetMetrics()

	queueDepth, _ := h.redis.QueueDepth(r.Context())

	metrics["queue_depth"] = queueDepth
	metrics["ws_clients"] = int64(h.hub.ConnectedClients())

	writeJSON(w, http.StatusOK, metrics)
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// readyBudget is shared by all probes and stays under the compose healthcheck's
// 5s timeout, so a hung dependency reports 503 instead of timing the check out.
const readyBudget = 3 * time.Second

// Ready reports whether dependencies answer, unlike Health, which only says the
// process is up. Probe errors are logged, never returned: they can carry
// hostnames or DSN fragments.
// GET /api/ready
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	probes := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"postgres", h.store.Pool().Ping},
		{"redis", h.redis.Ping},
	}
	if h.storage != nil {
		probes = append(probes, struct {
			name string
			fn   func(context.Context) error
		}{"minio", h.storage.Ping})
	}

	ctx, cancel := context.WithTimeout(r.Context(), readyBudget)
	defer cancel()
	checks := make(map[string]string, len(probes))
	ready := true
	for _, p := range probes {
		if err := p.fn(ctx); err != nil {
			ready = false
			checks[p.name] = "unavailable"
			logFrom(r).Warn().Err(err).Str("dependency", p.name).Msg("readiness check failed")
			continue
		}
		checks[p.name] = "ok"
	}

	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "checks": checks})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
}

// ==================== Helpers ====================

// ListTaskArtifacts returns the artifacts produced by a task execution.
// GET /api/tasks/{id}/artifacts
func (h *Handler) ListTaskArtifacts(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	task, err := h.store.GetTaskExecution(r.Context(), taskID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "task not found", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":       task.ID,
		"task_name":     task.TaskName,
		"artifacts_in":  task.ArtifactsIn,
		"artifacts_out": task.ArtifactsOut,
	})
}

// GetArtifactURL returns a pre-signed download URL for an artifact.
// GET /api/artifacts/url?key=artifacts/...&expires=60
func (h *Handler) GetArtifactURL(w http.ResponseWriter, r *http.Request) {
	if h.storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact storage not configured", nil)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, r, http.StatusBadRequest, "key parameter required", nil)
		return
	}
	expiresStr := r.URL.Query().Get("expires")
	expiresMins := 60
	if expiresStr != "" {
		if n, err := strconv.Atoi(expiresStr); err == nil && n > 0 {
			expiresMins = n
		}
	}
	url, err := h.storage.PresignURL(r.Context(), key, time.Duration(expiresMins)*time.Minute)
	if err != nil {
		logFrom(r).Error().Err(err).Str("key", key).Msg("presign failed")
		writeError(w, r, http.StatusInternalServerError, "could not generate download URL", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url, "key": key})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const errBodyTooLarge = "request body too large"

func isTooBig(err error) bool {
	var tooBig *http.MaxBytesError
	return errors.As(err, &tooBig)
}

// decodeJSON reads the request body into v, answering the client itself
// (413 or 400) and returning false when it cannot.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	switch {
	case err == nil:
		return true
	case isTooBig(err):
		writeError(w, r, http.StatusRequestEntityTooLarge, errBodyTooLarge, nil)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid request body", err)
	}
	return false
}

func writeError(w http.ResponseWriter, r *http.Request, status int, publicMsg string, internalErr error) {
	reqID, _ := r.Context().Value(RequestIDKey).(string)

	if internalErr != nil {
		// Securely log the real error on the backend, tied to the Request ID
		logFrom(r).Error().
			Err(internalErr).
			Int("status", status).
			Msg("api error")
	}

	// Return a safe message to the client, plus the ID
	writeJSON(w, status, map[string]string{
		"error":      publicMsg,
		"request_id": reqID,
	})
}

// maxPageLimit bounds a single page so one request cannot load the whole table.
const maxPageLimit = 200

// parsePagination reads limit/offset; malformed or negative values are an
// error (Postgres rejects a negative OFFSET, which would surface as a 500).
func parsePagination(r *http.Request, defaultLimit int) (limit, offset int, err error) {
	limit = defaultLimit
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
			return 0, 0, fmt.Errorf("invalid limit %q", v)
		}
		// limit=0 falls back to the default, as before.
		if limit == 0 {
			limit = defaultLimit
		}
	}
	if v := q.Get("offset"); v != "" {
		if offset, err = strconv.Atoi(v); err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("invalid offset %q", v)
		}
	}
	return min(limit, maxPageLimit), offset, nil
}
