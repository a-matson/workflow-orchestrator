package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/workflow-platform/backend/internal/models"
	"github.com/workflow-platform/backend/internal/orchestrator"
	"github.com/workflow-platform/backend/internal/persistence"
)

// Handler provides HTTP API endpoints for the workflow platform
type Handler struct {
	store        *persistence.Store
	redis        *persistence.RedisClient
	orchestrator *orchestrator.Orchestrator
	hub          *Hub
}

func NewHandler(store *persistence.Store, redis *persistence.RedisClient, orch *orchestrator.Orchestrator, hub *Hub) *Handler {
	return &Handler{store: store, redis: redis, orchestrator: orch, hub: hub}
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
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
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
		writeError(w, http.StatusInternalServerError, "failed to save workflow")
		return
	}

	writeJSON(w, http.StatusCreated, def)
}

func (h *Handler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	defs, err := h.store.ListWorkflowDefinitions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflows")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": defs, "count": len(defs)})
}

func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	def, err := h.store.GetWorkflowDefinition(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// ==================== Executions ====================

func (h *Handler) TriggerWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	def, err := h.store.GetWorkflowDefinition(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	var payload map[string]any
	_ = json.NewDecoder(r.Body).Decode(&payload)

	exec, err := h.orchestrator.StartWorkflow(r.Context(), def, payload)
	if err != nil {
		log.Error().Err(err).Str("workflow_id", id).Msg("failed to start workflow")
		writeError(w, http.StatusInternalServerError, "failed to start workflow: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, exec)
}

func (h *Handler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit == 0 {
		limit = 50
	}

	execs, err := h.store.ListWorkflowExecutions(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list executions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"executions": execs, "count": len(execs)})
}

func (h *Handler) GetExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exec, err := h.store.GetWorkflowExecution(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "execution not found")
		return
	}
	writeJSON(w, http.StatusOK, exec)
}

func (h *Handler) CancelExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exec, err := h.store.GetWorkflowExecution(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "execution not found")
		return
	}
	if exec.Status != models.WorkflowStatusRunning && exec.Status != models.WorkflowStatusPending {
		writeError(w, http.StatusConflict, "execution is not cancellable")
		return
	}
	now := time.Now()
	exec.Status = models.WorkflowStatusCancelled
	exec.CompletedAt = &now
	exec.UpdatedAt = now
	if err := h.store.UpdateWorkflowExecution(r.Context(), exec); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel execution")
		return
	}
	h.hub.Broadcast(models.WebSocketEvent{Type: models.WSEventWorkflowFailed, Payload: exec})
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled", "id": id})
}

func (h *Handler) RetryExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exec, err := h.store.GetWorkflowExecution(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "execution not found")
		return
	}

	def, err := h.store.GetWorkflowDefinition(r.Context(), exec.WorkflowID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "workflow definition not found")
		return
	}

	newExec, err := h.orchestrator.StartWorkflow(r.Context(), def, exec.TriggerPayload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retry: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, newExec)
}

// ==================== Tasks ====================

func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	execID := r.PathValue("execID")
	tasks, err := h.store.ListTaskExecutions(r.Context(), execID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "count": len(tasks)})
}

func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := h.store.GetTaskExecution(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *Handler) GetTaskLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := h.store.GetTaskExecution(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
