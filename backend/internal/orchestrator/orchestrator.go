package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/workflow-platform/backend/internal/dag"
	"github.com/workflow-platform/backend/internal/models"
	"github.com/workflow-platform/backend/internal/persistence"
	"github.com/workflow-platform/backend/internal/retry"
)

// Orchestrator is the central coordinator that:
// - Parses and validates workflow DAGs
// - Tracks execution state in PostgreSQL
// - Dispatches ready tasks to Redis
// - Processes results and advances workflows
// - Handles retries and dead-lettering
type Orchestrator struct {
	store       *persistence.Store
	redis       *persistence.RedisClient
	retryMgr    *retry.Manager
	broadcaster EventBroadcaster

	// In-memory state for active executions (keyed by workflow exec ID)
	activeMu sync.RWMutex
	active   map[string]*ExecutionContext

	// Concurrency semaphore per workflow (max parallel tasks)
	semaphores map[string]chan struct{}
	semMu      sync.Mutex

	metrics *Metrics
}

// ExecutionContext holds runtime state for an active workflow execution
type ExecutionContext struct {
	Execution  *models.WorkflowExecution
	Definition *models.WorkflowDefinition
	Graph      *dag.Graph
	TaskMap    map[string]*models.TaskExecution // taskDefID -> TaskExecution
	Completed  map[string]bool
	Running    map[string]bool
	Queued     map[string]bool
	Failed     map[string]bool
	mu         sync.RWMutex
}

// EventBroadcaster sends real-time updates to connected WebSocket clients
type EventBroadcaster interface {
	Broadcast(event models.WebSocketEvent)
}

// Metrics tracks orchestrator performance
type Metrics struct {
	WorkflowsStarted   int64
	WorkflowsCompleted int64
	WorkflowsFailed    int64
	TasksDispatched    int64
	TasksCompleted     int64
	TasksFailed        int64
	TasksRetried       int64
	TasksDeadLettered  int64
	mu                 sync.Mutex
}

func NewOrchestrator(store *persistence.Store, redis *persistence.RedisClient, broadcaster EventBroadcaster) *Orchestrator {
	return &Orchestrator{
		store:       store,
		redis:       redis,
		retryMgr:    retry.NewManager(),
		broadcaster: broadcaster,
		active:      make(map[string]*ExecutionContext),
		semaphores:  make(map[string]chan struct{}),
		metrics:     &Metrics{},
	}
}

// StartWorkflow validates the DAG, creates an execution record, and begins dispatching
func (o *Orchestrator) StartWorkflow(ctx context.Context, def *models.WorkflowDefinition, payload map[string]any) (*models.WorkflowExecution, error) {
	// Parse and validate the DAG
	graph, err := dag.Parse(def)
	if err != nil {
		return nil, fmt.Errorf("invalid workflow DAG: %w", err)
	}

	now := time.Now()
	exec := &models.WorkflowExecution{
		ID:             uuid.New().String(),
		WorkflowID:     def.ID,
		WorkflowName:   def.Name,
		Status:         models.WorkflowStatusPending,
		TriggerPayload: payload,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	// Persist execution record
	if err := o.store.CreateWorkflowExecution(ctx, exec); err != nil {
		return nil, fmt.Errorf("persisting execution: %w", err)
	}

	// Create task execution records for all tasks
	taskMap := make(map[string]*models.TaskExecution)
	for _, taskDef := range def.Tasks {
		maxRetries := 0
		if taskDef.RetryPolicy != nil {
			maxRetries = taskDef.RetryPolicy.MaxRetries
		} else if def.GlobalRetry != nil {
			maxRetries = def.GlobalRetry.MaxRetries
		}

		taskExec := &models.TaskExecution{
			ID:               uuid.New().String(),
			WorkflowExecID:   exec.ID,
			TaskDefinitionID: taskDef.ID,
			TaskName:         taskDef.Name,
			TaskType:         taskDef.Type,
			Status:           models.TaskStatusPending,
			MaxRetries:       maxRetries,
			CreatedAt:        now,
			UpdatedAt:        now,
		}

		if err := o.store.CreateTaskExecution(ctx, taskExec); err != nil {
			return nil, fmt.Errorf("creating task execution %s: %w", taskDef.ID, err)
		}

		taskMap[taskDef.ID] = taskExec
		exec.Tasks = append(exec.Tasks, taskExec)
	}

	// Build execution context
	execCtx := &ExecutionContext{
		Execution:  exec,
		Definition: def,
		Graph:      graph,
		TaskMap:    taskMap,
		Completed:  make(map[string]bool),
		Running:    make(map[string]bool),
		Queued:     make(map[string]bool),
		Failed:     make(map[string]bool),
	}

	// Register in active map
	o.activeMu.Lock()
	o.active[exec.ID] = execCtx
	o.activeMu.Unlock()

	// Initialize concurrency semaphore for this workflow
	maxParallel := def.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 10 // sensible default
	}
	o.semMu.Lock()
	o.semaphores[exec.ID] = make(chan struct{}, maxParallel)
	o.semMu.Unlock()

	// Transition to running
	exec.Status = models.WorkflowStatusRunning
	exec.StartedAt = &now
	exec.UpdatedAt = now
	if err := o.store.UpdateWorkflowExecution(ctx, exec); err != nil {
		log.Error().Err(err).Str("exec_id", exec.ID).Msg("failed to update workflow status")
	}

	o.metrics.mu.Lock()
	o.metrics.WorkflowsStarted++
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{
		Type:    models.WSEventWorkflowStarted,
		Payload: exec,
	})

	log.Info().
		Str("exec_id", exec.ID).
		Str("workflow_id", def.ID).
		Str("workflow_name", def.Name).
		Msg("workflow execution started")

	// Dispatch first wave of tasks (those with no dependencies)
	go o.dispatchReadyTasks(ctx, execCtx)

	return exec, nil
}

// dispatchReadyTasks finds all tasks whose dependencies are met and enqueues them
func (o *Orchestrator) dispatchReadyTasks(ctx context.Context, execCtx *ExecutionContext) {
	execCtx.mu.Lock()
	defer execCtx.mu.Unlock()

	readyTaskIDs := execCtx.Graph.GetReadyTasks(execCtx.Completed, execCtx.Running, execCtx.Queued)

	for _, taskDefID := range readyTaskIDs {
		taskExec := execCtx.TaskMap[taskDefID]
		taskDef := execCtx.Graph.Nodes[taskDefID].Task

		// Acquire concurrency slot
		sem := o.getSemaphore(execCtx.Execution.ID)
		select {
		case sem <- struct{}{}:
		default:
			// Semaphore full — will be dispatched when a slot opens
			log.Debug().
				Str("task_id", taskDefID).
				Msg("concurrency limit reached, task deferred")
			continue
		}

		execCtx.Queued[taskDefID] = true

		msg := &models.TaskMessage{
			TaskExecID:       taskExec.ID,
			WorkflowExecID:   execCtx.Execution.ID,
			WorkflowID:       execCtx.Execution.WorkflowID,
			TaskDefinitionID: taskDefID,
			TaskName:         taskDef.Name,
			TaskType:         taskDef.Type,
			Config:           taskDef.Config,
			RetryCount:       taskExec.RetryCount,
			MaxRetries:       taskExec.MaxRetries,
			Timeout:          taskDef.Timeout,
			EnqueuedAt:       time.Now(),
			IdempotencyKey:   fmt.Sprintf("%s:%s:%d", execCtx.Execution.ID, taskExec.ID, taskExec.RetryCount),
		}

		if err := o.redis.EnqueueTask(ctx, msg); err != nil {
			log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("failed to enqueue task")
			<-sem // Release semaphore on failure
			execCtx.Queued[taskDefID] = false
			continue
		}

		// Update task state to queued
		now := time.Now()
		taskExec.Status = models.TaskStatusQueued
		taskExec.QueuedAt = &now
		taskExec.UpdatedAt = now

		if err := o.store.UpdateTaskExecution(ctx, taskExec); err != nil {
			log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("failed to persist task state")
		}

		o.metrics.mu.Lock()
		o.metrics.TasksDispatched++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(models.WebSocketEvent{
			Type:    models.WSEventTaskQueued,
			Payload: taskExec,
		})

		log.Info().
			Str("task_exec_id", taskExec.ID).
			Str("task_name", taskDef.Name).
			Str("workflow_exec_id", execCtx.Execution.ID).
			Msg("task dispatched to queue")
	}
}

// ProcessResult handles a task completion result from a worker
func (o *Orchestrator) ProcessResult(ctx context.Context, result *models.TaskResult) error {
	o.activeMu.RLock()
	execCtx, exists := o.active[result.WorkflowExecID]
	o.activeMu.RUnlock()

	if !exists {
		// Execution not in memory — reload from DB (e.g. after restart)
		return o.handleOrphanedResult(ctx, result)
	}

	taskExec, err := o.store.GetTaskExecution(ctx, result.TaskExecID)
	if err != nil {
		return fmt.Errorf("getting task execution: %w", err)
	}

	// Determine task definition ID from task exec
	taskDefID := taskExec.TaskDefinitionID

	// Release concurrency slot
	sem := o.getSemaphore(result.WorkflowExecID)
	select {
	case <-sem:
	default:
	}

	if result.Success {
		return o.handleTaskSuccess(ctx, execCtx, taskExec, taskDefID, result)
	}
	return o.handleTaskFailure(ctx, execCtx, taskExec, taskDefID, result)
}

func (o *Orchestrator) handleTaskSuccess(ctx context.Context, execCtx *ExecutionContext, taskExec *models.TaskExecution, taskDefID string, result *models.TaskResult) error {
	now := time.Now()
	taskExec.Status = models.TaskStatusCompleted
	taskExec.Output = result.Output
	taskExec.Logs = append(taskExec.Logs, result.Logs...)
	taskExec.CompletedAt = &now
	taskExec.WorkerID = result.WorkerID
	taskExec.UpdatedAt = now

	dur := now.Sub(result.StartedAt)
	taskExec.Duration = &dur

	if err := o.store.UpdateTaskExecution(ctx, taskExec); err != nil {
		return fmt.Errorf("persisting task completion: %w", err)
	}

	// Mark idempotency key as processed
	_ = o.redis.SetIdempotency(ctx, fmt.Sprintf("%s:%s:%d", execCtx.Execution.ID, taskExec.ID, taskExec.RetryCount), 24*time.Hour)

	execCtx.mu.Lock()
	execCtx.Completed[taskDefID] = true
	delete(execCtx.Running, taskDefID)
	delete(execCtx.Queued, taskDefID)
	execCtx.mu.Unlock()

	o.metrics.mu.Lock()
	o.metrics.TasksCompleted++
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{
		Type:    models.WSEventTaskCompleted,
		Payload: taskExec,
	})

	log.Info().
		Str("task_exec_id", taskExec.ID).
		Str("task_name", taskExec.TaskName).
		Str("worker_id", result.WorkerID).
		Msg("task completed successfully")

	// Check if all tasks are done
	if o.isWorkflowComplete(execCtx) {
		return o.completeWorkflow(ctx, execCtx, false)
	}

	// Dispatch next wave of now-unblocked tasks
	go o.dispatchReadyTasks(ctx, execCtx)
	return nil
}

func (o *Orchestrator) handleTaskFailure(ctx context.Context, execCtx *ExecutionContext, taskExec *models.TaskExecution, taskDefID string, result *models.TaskResult) error {
	now := time.Now()

	// Get retry policy
	var policy *models.RetryPolicy
	if node, ok := execCtx.Graph.Nodes[taskDefID]; ok {
		policy = node.Task.RetryPolicy
	}
	if policy == nil {
		policy = execCtx.Definition.GlobalRetry
	}
	if policy == nil {
		policy = &retry.DefaultPolicy
	}

	taskExec.Logs = append(taskExec.Logs, result.Logs...)
	taskExec.Error = result.Error
	taskExec.WorkerID = result.WorkerID
	taskExec.CompletedAt = &now

	if o.retryMgr.ShouldRetry(taskExec, policy) {
		o.retryMgr.ScheduleRetry(ctx, taskExec, policy, result.Error)
		taskExec.UpdatedAt = now

		if err := o.store.UpdateTaskExecution(ctx, taskExec); err != nil {
			return fmt.Errorf("persisting retry state: %w", err)
		}

		// Schedule retry in Redis
		msg := &models.TaskMessage{
			TaskExecID:       taskExec.ID,
			WorkflowExecID:   execCtx.Execution.ID,
			WorkflowID:       execCtx.Execution.WorkflowID,
			TaskDefinitionID: taskDefID,
			TaskName:         taskExec.TaskName,
			TaskType:         taskExec.TaskType,
			RetryCount:       taskExec.RetryCount,
			MaxRetries:       taskExec.MaxRetries,
			IdempotencyKey:   fmt.Sprintf("%s:%s:%d", execCtx.Execution.ID, taskExec.ID, taskExec.RetryCount),
		}

		if taskExec.NextRetryAt != nil {
			_ = o.redis.ScheduleRetry(ctx, msg, *taskExec.NextRetryAt)
		}

		o.metrics.mu.Lock()
		o.metrics.TasksRetried++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(models.WebSocketEvent{
			Type:    models.WSEventTaskRetrying,
			Payload: taskExec,
		})

		execCtx.mu.Lock()
		delete(execCtx.Queued, taskDefID)
		delete(execCtx.Running, taskDefID)
		execCtx.mu.Unlock()

	} else {
		// No more retries — dead letter
		taskExec.Status = models.TaskStatusDeadLetter
		taskExec.UpdatedAt = now
		_ = o.store.UpdateTaskExecution(ctx, taskExec)

		_ = o.redis.SendToDeadLetter(ctx, &models.TaskMessage{
			TaskExecID:     taskExec.ID,
			WorkflowExecID: execCtx.Execution.ID,
		}, result.Error)

		execCtx.mu.Lock()
		execCtx.Failed[taskDefID] = true
		delete(execCtx.Queued, taskDefID)
		delete(execCtx.Running, taskDefID)
		execCtx.mu.Unlock()

		o.metrics.mu.Lock()
		o.metrics.TasksFailed++
		o.metrics.TasksDeadLettered++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(models.WebSocketEvent{
			Type:    models.WSEventTaskFailed,
			Payload: taskExec,
		})

		// Fail the entire workflow
		return o.completeWorkflow(ctx, execCtx, true)
	}

	return nil
}

func (o *Orchestrator) isWorkflowComplete(execCtx *ExecutionContext) bool {
	execCtx.mu.RLock()
	defer execCtx.mu.RUnlock()

	totalTasks := len(execCtx.Graph.Nodes)
	return len(execCtx.Completed) == totalTasks
}

func (o *Orchestrator) completeWorkflow(ctx context.Context, execCtx *ExecutionContext, failed bool) error {
	now := time.Now()
	execCtx.Execution.CompletedAt = &now
	execCtx.Execution.UpdatedAt = now

	evtType := models.WSEventWorkflowCompleted
	if failed {
		execCtx.Execution.Status = models.WorkflowStatusFailed
		evtType = models.WSEventWorkflowFailed

		o.metrics.mu.Lock()
		o.metrics.WorkflowsFailed++
		o.metrics.mu.Unlock()
	} else {
		execCtx.Execution.Status = models.WorkflowStatusCompleted

		o.metrics.mu.Lock()
		o.metrics.WorkflowsCompleted++
		o.metrics.mu.Unlock()
	}

	if err := o.store.UpdateWorkflowExecution(ctx, execCtx.Execution); err != nil {
		return fmt.Errorf("persisting workflow completion: %w", err)
	}

	// Cleanup in-memory state
	o.activeMu.Lock()
	delete(o.active, execCtx.Execution.ID)
	o.activeMu.Unlock()

	o.semMu.Lock()
	delete(o.semaphores, execCtx.Execution.ID)
	o.semMu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{
		Type:    evtType,
		Payload: execCtx.Execution,
	})

	log.Info().
		Str("exec_id", execCtx.Execution.ID).
		Str("status", string(execCtx.Execution.Status)).
		Msg("workflow execution completed")

	return nil
}

func (o *Orchestrator) getSemaphore(workflowExecID string) chan struct{} {
	o.semMu.Lock()
	defer o.semMu.Unlock()
	return o.semaphores[workflowExecID]
}

func (o *Orchestrator) handleOrphanedResult(ctx context.Context, result *models.TaskResult) error {
	log.Warn().
		Str("task_exec_id", result.TaskExecID).
		Str("workflow_exec_id", result.WorkflowExecID).
		Msg("received result for non-active workflow — reloading from DB")

	// Reload state from database (after restart recovery)
	exec, err := o.store.GetWorkflowExecution(ctx, result.WorkflowExecID)
	if err != nil {
		return fmt.Errorf("reloading workflow execution: %w", err)
	}

	if exec.Status == models.WorkflowStatusCompleted || exec.Status == models.WorkflowStatusFailed {
		return nil // Already done
	}

	// TODO: Full state reconstruction from DB for crash recovery
	log.Info().Str("exec_id", exec.ID).Msg("workflow state reloaded from DB")
	return nil
}

// MarkTaskRunning updates task state when a worker picks it up
func (o *Orchestrator) MarkTaskRunning(ctx context.Context, taskExecID, workerID string) error {
	taskExec, err := o.store.GetTaskExecution(ctx, taskExecID)
	if err != nil {
		return err
	}

	now := time.Now()
	taskExec.Status = models.TaskStatusRunning
	taskExec.WorkerID = workerID
	taskExec.StartedAt = &now
	taskExec.UpdatedAt = now

	if err := o.store.UpdateTaskExecution(ctx, taskExec); err != nil {
		return err
	}

	o.activeMu.RLock()
	if execCtx, ok := o.active[taskExec.WorkflowExecID]; ok {
		execCtx.mu.Lock()
		execCtx.Running[taskExec.TaskDefinitionID] = true
		delete(execCtx.Queued, taskExec.TaskDefinitionID)
		execCtx.mu.Unlock()
	}
	o.activeMu.RUnlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{
		Type:    models.WSEventTaskStarted,
		Payload: taskExec,
	})

	return nil
}

// GetMetrics returns current orchestrator metrics
func (o *Orchestrator) GetMetrics() map[string]int64 {
	o.metrics.mu.Lock()
	defer o.metrics.mu.Unlock()

	return map[string]int64{
		"workflows_started":   o.metrics.WorkflowsStarted,
		"workflows_completed": o.metrics.WorkflowsCompleted,
		"workflows_failed":    o.metrics.WorkflowsFailed,
		"tasks_dispatched":    o.metrics.TasksDispatched,
		"tasks_completed":     o.metrics.TasksCompleted,
		"tasks_failed":        o.metrics.TasksFailed,
		"tasks_retried":       o.metrics.TasksRetried,
		"tasks_dead_lettered": o.metrics.TasksDeadLettered,
		"active_workflows":    int64(len(o.active)),
	}
}
