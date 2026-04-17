package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

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
}

func NewHandler(store *persistence.Store, redis *persistence.RedisClient, orch *orchestrator.Orchestrator, hub *Hub) *Handler {
	return &Handler{store: store, redis: redis, orchestrator: orch, hub: hub}
}

func NewHandlerWithStorage(store *persistence.Store, redis *persistence.RedisClient, orch *orchestrator.Orchestrator, hub *Hub, sc *storage.Client) *Handler {
	return &Handler{store: store, redis: redis, orchestrator: orch, hub: hub, storage: sc}
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Workflow definitions
	mux.HandleFunc("POST /api/workflows", h.CreateWorkflow)
	mux.HandleFunc("GET /api/workflows", h.ListWorkflows)
	mux.HandleFunc("GET /api/workflows/{id}", h.GetWorkflow)

	// Workflow executions
	mux.HandleFunc("POST /api/workflows/{id}/trigger", h.TriggerWorkflow)
	mux.HandleFunc("GET /api/executions", h.ListExecutions)
	mux.HandleFunc("GET /api/executions/{id}", h.GetExecution)
	mux.HandleFunc("POST /api/executions/{id}/cancel", h.CancelExecution)
	mux.HandleFunc("POST /api/executions/{id}/retry", h.RetryExecution)

	// Task executions
	mux.HandleFunc("GET /api/executions/{execID}/tasks", h.ListTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.GetTask)
	mux.HandleFunc("GET /api/tasks/{id}/logs", h.GetTaskLogs)

	// System
	mux.HandleFunc("GET /api/metrics", h.GetMetrics)
	mux.HandleFunc("GET /api/health", h.Health)

	// Artifacts
	mux.HandleFunc("GET /api/tasks/{id}/artifacts", h.ListTaskArtifacts)
	mux.HandleFunc("GET /api/artifacts/url", h.GetArtifactURL)

	// WebSocket
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		h.hub.ServeWS(w, r)
	})

	return mux
}

// ==================== Workflow Definitions ====================

func (h *Handler) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var def models.WorkflowDefinition
	if err := json.NewDecoder(r.Body).Decode(&def); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid request body: "+err.Error(), err)
		return
	}

	if def.ID == "" {
		def.ID = uuid.New().String()
	}
	now := time.Now()
	def.CreatedAt = now
	def.UpdatedAt = now

	if err := h.store.SaveWorkflowDefinition(r.Context(), &def); err != nil {
		log.Error().Err(err).Msg("failed to save workflow definition")
		writeError(w, r, http.StatusInternalServerError, "failed to save workflow", err)
		return
	}

	writeJSON(w, http.StatusCreated, def)
}

func (h *Handler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	defs, err := h.store.ListWorkflowDefinitions(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to list workflows", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": defs, "count": len(defs)})
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
	_ = json.NewDecoder(r.Body).Decode(&payload)

	exec, err := h.orchestrator.StartWorkflow(r.Context(), def, payload)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to start workflow", err)
		return
	}

	writeJSON(w, http.StatusAccepted, exec)
}

func (h *Handler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r, 50)

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
	retryDepth, _ := h.redis.RetryQueueDepth(r.Context())
	redisMetrics, _ := h.redis.GetMetrics(r.Context())

	metrics["queue_depth"] = queueDepth
	metrics["retry_queue_depth"] = retryDepth
	metrics["ws_clients"] = int64(h.hub.ConnectedClients())
	_ = redisMetrics

	writeJSON(w, http.StatusOK, metrics)
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// ==================== Helpers ====================

// ListTaskArtifacts returns the artifacts produced by a task execution.
// GET /api/tasks/{id}/artifacts
func (h *Handler) ListTaskArtifacts(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	task, err := h.store.GetTaskExecution(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
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
		writeError(w, http.StatusServiceUnavailable, "artifact storage not configured")
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key parameter required")
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
		log.Error().Err(err).Str("key", key).Msg("presign failed")
		writeError(w, http.StatusInternalServerError, "could not generate download URL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url, "key": key})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, publicMsg string, internalErr error) {
	reqID, _ := r.Context().Value(RequestIDKey).(string)

	if internalErr != nil {
		// Securely log the real error on the backend, tied to the Request ID
		log.Error().
			Err(internalErr).
			Str("request_id", reqID).
			Int("status", status).
			Msg("api error")
	}

	// Return a safe message to the client, plus the ID
	writeJSON(w, status, map[string]string{
		"error":      publicMsg,
		"request_id": reqID,
	})
}

func parsePagination(r *http.Request, defaultLimit int) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultLimit
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	return limit, offset
}
