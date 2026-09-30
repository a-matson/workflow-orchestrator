package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

		"POST /api/client-errors": h.ReportClientError,

		// Artifacts
		"GET /api/tasks/{id}/artifacts":           h.ListTaskArtifacts,
		"GET /api/tasks/{id}/artifacts/{path...}": h.DownloadTaskArtifact,

		// API keys
		"GET /api/keys":         h.ListAPIKeys,
		"POST /api/keys":        h.CreateAPIKey,
		"DELETE /api/keys/{id}": h.RevokeAPIKey,

		"GET /api/audit": h.ListAudit,

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
		h.audit(r, "workflow.create", "workflow", def.ID, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to save workflow", err)
		return
	}
	h.audit(r, "workflow.create", "workflow", def.ID, auditSuccess)

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
		h.audit(r, "workflow.update", "workflow", id, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to update workflow", err)
		return
	}
	h.audit(r, "workflow.update", "workflow", id, auditSuccess)
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
		h.audit(r, "workflow.trigger", "workflow", id, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to start workflow", err)
		return
	}
	h.audit(r, "workflow.trigger", "workflow", id, auditSuccess)

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
	// Read first only to tell a missing execution from a final one; the
	// transition's conflict covers both.
	if _, err := h.store.GetWorkflowExecution(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "execution not found", err)
		return
	} else if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to load execution", err)
		return
	}
	// Detached so a client hanging up cannot abort the cancel after its
	// transaction commits, and skip the cache update and event.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	exec, err := h.orchestrator.CancelExecution(ctx, id)
	if errors.Is(err, persistence.ErrConflict) {
		writeError(w, r, http.StatusConflict, "execution is not cancellable", err)
		return
	}
	// A 409 changed nothing, so like the other refusals it is not audited.
	if err != nil {
		h.audit(r, "execution.cancel", "execution", id, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to cancel execution", err)
		return
	}
	h.audit(r, "execution.cancel", "execution", id, auditSuccess)
	writeJSON(w, http.StatusOK, exec)
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
		h.audit(r, "execution.retry", "execution", id, auditError)
		writeError(w, r, http.StatusInternalServerError, "failed to retry: "+err.Error(), err)
		return
	}
	h.audit(r, "execution.retry", "execution", id, auditSuccess)

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

// DownloadTaskArtifact streams one artifact the task produced. The object key
// comes from the task's own row, never from the request, so a caller can reach
// only artifacts of a task, by the path the task declared.
// GET /api/tasks/{id}/artifacts/{path...}
func (h *Handler) DownloadTaskArtifact(w http.ResponseWriter, r *http.Request) {
	if h.storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact storage not configured", nil)
		return
	}
	task, err := h.store.GetTaskExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "task not found", err)
		return
	}
	path := r.PathValue("path")
	i := slices.IndexFunc(task.ArtifactsOut, func(a models.ResolvedArtifact) bool { return a.Path == path })
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "artifact not found", nil)
		return
	}
	body, size, err := h.storage.Download(r.Context(), task.ArtifactsOut[i].MinioKey)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "could not read artifact", err)
		return
	}
	defer func() {
		if err := body.Close(); err != nil {
			logFrom(r).Warn().Err(err).Msg("closing artifact stream")
		}
	}()

	// The server's 60s WriteTimeout would cut off a large artifact; a bound
	// remains so a stalled client cannot hold the stream open forever.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute)); err != nil {
		logFrom(r).Warn().Err(err).Msg("could not extend the artifact write deadline")
	}
	// Served as an opaque download, never rendered: the content is task output.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		logFrom(r).Warn().Err(err).Str("task_id", task.ID).Msg("artifact download interrupted")
	}
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
