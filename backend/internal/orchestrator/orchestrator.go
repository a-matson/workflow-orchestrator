package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/dag"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/retry"
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

// ExecutionContext holds runtime state for one active workflow execution.
// mu guards the Completed/Running/Queued/Failed maps, TaskMap, the task rows
// in Execution.Tasks, and done. dispatchReadyTasks and MarkTaskRunning hold it
// across store calls so their writes to a task row land in order.
// Never call any method that re-acquires this mutex while holding it.
type ExecutionContext struct {
	Execution  *models.WorkflowExecution
	Definition *models.WorkflowDefinition
	Graph      *dag.Graph
	TaskMap    map[string]*models.TaskExecution // taskDefID -> TaskExecution
	Completed  map[string]bool
	Running    map[string]bool
	Queued     map[string]bool
	Failed     map[string]bool
	// done is set once completeWorkflow has claimed the execution; a late
	// result must not change the rows its final event reported.
	done bool
	mu   sync.Mutex
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
		ID:           uuid.New().String(),
		WorkflowID:   def.ID,
		WorkflowName: def.Name,
		// Inserted as running: the insert is atomic with the task rows, so no
		// reader can observe a pending window, and a follow-up transition would
		// be a second write that could fail after the commit.
		Status:         models.WorkflowStatusRunning,
		TriggerPayload: payload,
		StartedAt:      &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

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
		taskMap[taskDef.ID] = taskExec
		exec.Tasks = append(exec.Tasks, taskExec)
	}

	if err := o.store.CreateExecutionWithTasks(ctx, exec, exec.Tasks); err != nil {
		return nil, fmt.Errorf("persisting execution and tasks: %w", err)
	}

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

	// Taken before the context is shared: dispatch starts writing the live
	// rows as soon as it is registered.
	snap := execCtx.snapshot()

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

	o.metrics.mu.Lock()
	o.metrics.WorkflowsStarted++
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventWorkflowStarted, Payload: snap})

	log.Info().Str("exec_id", exec.ID).Str("workflow", def.Name).Msg("workflow execution started")

	// Dispatch first wave of tasks (those with no dependencies)
	dispatchCtx := context.WithoutCancel(ctx)
	goSafe("dispatch", exec.ID, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })

	return snap, nil
}

// ── dispatchReadyTasks ─────────────────────────────────────────────────────
// Acquires the execution context lock, finds all tasks whose dependencies are
// satisfied, and enqueues them into Redis. Called after start and after each
// successful task completion.

func (o *Orchestrator) dispatchReadyTasks(ctx context.Context, execCtx *ExecutionContext) {
	execCtx.mu.Lock()
	defer execCtx.mu.Unlock()

	readyTaskIDs := execCtx.Graph.GetReadyTasks(execCtx.Completed, execCtx.Running, execCtx.Queued)

	for _, taskDefID := range readyTaskIDs {
		taskExec := execCtx.TaskMap[taskDefID]
		taskDef := execCtx.Graph.Nodes[taskDefID].Task

		// Acquire concurrency slot
		sem := o.getSemaphore(execCtx.Execution.ID)
		if sem == nil {
			// Workflow already cleaned up (cancelled/completed race)
			return
		}
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

		// Resolve artifact inputs: for each ArtifactsIn spec, find the
		// ResolvedArtifact produced by the dependency task that matches the path.
		resolvedIn := o.resolveArtifactsIn(execCtx, taskDefID, taskDef.ArtifactsIn)

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
			Container:        taskDef.Container,
			ArtifactsIn:      resolvedIn,
			ArtifactsOut:     taskDef.ArtifactsOut,
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
			log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("failed to persist task queued state")
		}

		o.metrics.mu.Lock()
		o.metrics.TasksDispatched++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventTaskQueued, Payload: taskExec})
		log.Info().Str("task_exec_id", taskExec.ID).Str("task_name", taskDef.Name).Msg("task dispatched")
	}
}

// Broadcasts a single log entry immediately as a task.log WebSocket event.
// Called by the worker for each line so the UI streams terminal output live.
func (o *Orchestrator) StreamLog(workflowExecID, taskExecID, taskName string, entry models.LogEntry) {
	o.broadcaster.Broadcast(models.WebSocketEvent{
		Type: models.WSEventTaskLog,
		Payload: map[string]any{
			"workflow_exec_id": workflowExecID,
			"task_exec_id":     taskExecID,
			"task_name":        taskName,
			"entry":            entry,
		},
	})
}

// Called by the worker (via the API or directly) when it picks up a task.
// Moves the task from Queued → Running so dispatchReadyTasks knows the slot
// is actively occupied and the task won't be re-dispatched.
func (o *Orchestrator) MarkTaskRunning(ctx context.Context, taskExecID, workerID string) error {
	taskExec, err := o.store.GetTaskExecution(ctx, taskExecID)
	if err != nil {
		return err
	}

	o.activeMu.RLock()
	execCtx, ok := o.active[taskExec.WorkflowExecID]
	o.activeMu.RUnlock()
	if ok {
		// dispatchReadyTasks persists "queued" after the enqueue, under
		// execCtx.mu, so a fast worker can get here first. Its full-row write
		// would then put the row back to queued, and a queued task's result
		// is rejected (completion requires running). Wait for dispatch and
		// re-read so this write lands last.
		execCtx.mu.Lock()
		defer execCtx.mu.Unlock()
		if taskExec, err = o.store.GetTaskExecution(ctx, taskExecID); err != nil {
			return err
		}
	}

	// Not TransitionTask: a retry is re-enqueued with its row still in
	// retrying, and retrying -> running is not an allowed transition.
	now := time.Now()
	taskExec.Status = models.TaskStatusRunning
	taskExec.WorkerID = workerID
	taskExec.StartedAt = &now
	taskExec.UpdatedAt = now
	if err := o.store.UpdateTaskExecution(ctx, taskExec); err != nil {
		return err
	}

	if ok {
		execCtx.Running[taskExec.TaskDefinitionID] = true
		delete(execCtx.Queued, taskExec.TaskDefinitionID)
	}

	o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventTaskStarted, Payload: taskExec})
	return nil
}

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

	if result.Success {
		return o.handleTaskSuccess(ctx, execCtx, taskDefID, result)
	}
	return o.handleTaskFailure(ctx, execCtx, taskExec, taskDefID, result)
}

func (o *Orchestrator) handleTaskSuccess(ctx context.Context, execCtx *ExecutionContext, taskDefID string, result *models.TaskResult) error {
	now := time.Now()
	taskExec, err := o.transitionResult(ctx, result, models.TaskStatusCompleted, persistence.TaskPatch{
		WorkerID:     result.WorkerID,
		CompletedAt:  &now,
		Output:       result.Output,
		Logs:         result.Logs,
		ArtifactsOut: result.ArtifactsOut,
	})
	if err != nil {
		return dropStaleResult(result, models.TaskStatusCompleted, err)
	}
	// Duration has no column; it only rides on the broadcast payload.
	dur := now.Sub(result.StartedAt)
	taskExec.Duration = &dur
	o.releaseSlot(execCtx.Execution.ID)

	// Mark idempotency so re-delivered messages are no-ops
	_ = o.redis.SetIdempotency(ctx,
		fmt.Sprintf("%s:%s:%d", execCtx.Execution.ID, taskExec.ID, taskExec.RetryCount),
		24*time.Hour)

	// Update in-memory state — acquire lock, update maps, release, then act
	execCtx.mu.Lock()
	execCtx.cacheTask(taskExec)
	execCtx.Completed[taskDefID] = true
	delete(execCtx.Running, taskDefID)
	delete(execCtx.Queued, taskDefID)
	totalTasks := len(execCtx.Graph.Nodes)
	completedCount := len(execCtx.Completed)
	execCtx.mu.Unlock()
	// Lock is now released — safe to call other methods

	o.metrics.mu.Lock()
	o.metrics.TasksCompleted++
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventTaskCompleted, Payload: taskExec})
	log.Info().Str("task_exec_id", taskExec.ID).Str("task_name", taskExec.TaskName).Msg("task completed")

	// Check completion and dispatch next wave without holding any lock
	if completedCount == totalTasks {
		return o.completeWorkflow(ctx, execCtx, false)
	}

	// Dispatch next wave of now-unblocked tasks
	dispatchCtx := context.WithoutCancel(ctx)
	goSafe("dispatch", execCtx.Execution.ID, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })
	return nil
}

func (o *Orchestrator) handleTaskFailure(ctx context.Context, execCtx *ExecutionContext, taskExec *models.TaskExecution, taskDefID string, result *models.TaskResult) error {
	now := time.Now()

	// Resolve retry policy
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

	patch := persistence.TaskPatch{
		WorkerID:    result.WorkerID,
		CompletedAt: &now,
		Error:       &result.Error,
		Logs:        result.Logs,
	}

	if o.retryMgr.ShouldRetry(taskExec, policy) {
		// ScheduleRetry only computes the next attempt on this read copy;
		// the transition below is what persists it.
		o.retryMgr.ScheduleRetry(ctx, taskExec, policy, result.Error)
		patch.RetryCount = &taskExec.RetryCount
		patch.NextRetryAt = taskExec.NextRetryAt

		taskExec, err := o.transitionResult(ctx, result, models.TaskStatusRetrying, patch)
		if err != nil {
			return dropStaleResult(result, models.TaskStatusRetrying, err)
		}
		o.releaseSlot(execCtx.Execution.ID)

		// Schedule retry in Redis
		taskDef := execCtx.Graph.Nodes[taskDefID].Task
		// Re-resolve artifact inputs for the retry: the dependency outputs are
		// still in the TaskMap from the original run.
		execCtx.mu.Lock()
		execCtx.cacheTask(taskExec)
		retryArtifactsIn := o.resolveArtifactsIn(execCtx, taskDefID, taskDef.ArtifactsIn)
		execCtx.mu.Unlock()

		msg := &models.TaskMessage{
			TaskExecID:       taskExec.ID,
			WorkflowExecID:   execCtx.Execution.ID,
			WorkflowID:       execCtx.Execution.WorkflowID,
			TaskDefinitionID: taskDefID,
			TaskName:         taskExec.TaskName,
			TaskType:         taskExec.TaskType,
			Config:           taskDef.Config,
			RetryCount:       taskExec.RetryCount,
			MaxRetries:       taskExec.MaxRetries,
			Timeout:          taskDef.Timeout,
			IdempotencyKey:   fmt.Sprintf("%s:%s:%d", execCtx.Execution.ID, taskExec.ID, taskExec.RetryCount),
			Container:        taskDef.Container,
			ArtifactsIn:      retryArtifactsIn,
			ArtifactsOut:     taskDef.ArtifactsOut,
		}

		if taskExec.NextRetryAt != nil {
			if err := o.redis.ScheduleRetry(ctx, msg, *taskExec.NextRetryAt); err != nil {
				log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("failed to schedule task retry")
			}
		}

		o.metrics.mu.Lock()
		o.metrics.TasksRetried++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventTaskRetrying, Payload: taskExec})

		execCtx.mu.Lock()
		delete(execCtx.Queued, taskDefID)
		delete(execCtx.Running, taskDefID)
		execCtx.mu.Unlock()

	} else {
		// Exhausted retries → dead letter
		taskExec, err := o.transitionResult(ctx, result, models.TaskStatusDeadLetter, patch)
		if err != nil {
			return dropStaleResult(result, models.TaskStatusDeadLetter, err)
		}
		o.releaseSlot(execCtx.Execution.ID)

		if err := o.redis.SendToDeadLetter(ctx, &models.TaskMessage{
			TaskExecID:     taskExec.ID,
			WorkflowExecID: execCtx.Execution.ID,
		}, result.Error); err != nil {
			log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("failed to record task in dead-letter queue")
		}

		execCtx.mu.Lock()
		execCtx.cacheTask(taskExec)
		execCtx.Failed[taskDefID] = true
		delete(execCtx.Queued, taskDefID)
		delete(execCtx.Running, taskDefID)
		execCtx.mu.Unlock()

		o.metrics.mu.Lock()
		o.metrics.TasksFailed++
		o.metrics.TasksDeadLettered++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventTaskFailed, Payload: taskExec})

		// Fail the entire workflow
		return o.completeWorkflow(ctx, execCtx, true)
	}

	return nil
}

func (o *Orchestrator) completeWorkflow(ctx context.Context, execCtx *ExecutionContext, failed bool) error {
	now := time.Now()
	status, evtType := models.WorkflowStatusCompleted, models.WSEventWorkflowCompleted
	if failed {
		status, evtType = models.WorkflowStatusFailed, models.WSEventWorkflowFailed
	}

	execCtx.mu.Lock()
	if execCtx.done {
		// A result that raced the first completion; the execution already
		// has its final status and event.
		execCtx.mu.Unlock()
		return nil
	}
	execCtx.done = true
	execCtx.Execution.CompletedAt = &now
	execCtx.Execution.UpdatedAt = now
	execCtx.Execution.Status = status
	final := execCtx.snapshot()
	execCtx.mu.Unlock()

	o.metrics.mu.Lock()
	if failed {
		o.metrics.WorkflowsFailed++
	} else {
		o.metrics.WorkflowsCompleted++
	}
	o.metrics.mu.Unlock()

	if err := o.store.UpdateWorkflowExecution(ctx, final); err != nil {
		return fmt.Errorf("persisting workflow completion: %w", err)
	}

	// Cleanup in-memory state
	o.activeMu.Lock()
	delete(o.active, execCtx.Execution.ID)
	o.activeMu.Unlock()

	o.semMu.Lock()
	delete(o.semaphores, execCtx.Execution.ID)
	o.semMu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{Type: evtType, Payload: final})
	log.Info().Str("exec_id", final.ID).Str("status", string(final.Status)).Msg("workflow finished")
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

// cacheTask replaces the cached row for task's definition, in TaskMap and in
// Execution.Tasks. It swaps pointers rather than copying fields because
// earlier task events may still be reading the old row. It is a no-op once
// the execution is done. Callers hold c.mu.
func (c *ExecutionContext) cacheTask(task *models.TaskExecution) {
	if c.done {
		return
	}
	c.TaskMap[task.TaskDefinitionID] = task
	for i, t := range c.Execution.Tasks {
		if t.TaskDefinitionID == task.TaskDefinitionID {
			c.Execution.Tasks[i] = task
			return
		}
	}
}

// snapshot copies the execution, its task slice and each task row, for use
// as an event payload. Consumers marshal payloads later on their own
// goroutine (api.Hub.Run), while cacheTask, dispatch and completeWorkflow
// keep writing the live copies. Callers hold c.mu or have not shared c yet.
func (c *ExecutionContext) snapshot() *models.WorkflowExecution {
	snap := *c.Execution
	snap.Tasks = make([]*models.TaskExecution, len(c.Execution.Tasks))
	for i, t := range c.Execution.Tasks {
		row := *t
		snap.Tasks[i] = &row
	}
	return &snap
}

// transitionResult applies a result's transition to task result.TaskExecID.
// The worker runs a task even when its pickup write fails (worker.go logs and
// carries on), so a row still queued at the result's attempt has only missed
// that write: it is moved to running and the transition is tried once more.
// Any other conflict is returned for dropStaleResult.
func (o *Orchestrator) transitionResult(ctx context.Context, result *models.TaskResult, to models.TaskStatus, p persistence.TaskPatch) (*models.TaskExecution, error) {
	row, err := o.store.TransitionTask(ctx, result.TaskExecID, result.Attempt(), to, p)
	if !errors.Is(err, persistence.ErrConflict) {
		return row, err
	}
	cur, getErr := o.store.GetTaskExecution(ctx, result.TaskExecID)
	if getErr != nil {
		// Not wrapping err: a failed re-read is not a stale result, and
		// dropStaleResult would discard anything that matches ErrConflict.
		return nil, fmt.Errorf("re-reading task after a transition conflict: %w", getErr)
	}
	if cur.Status != models.TaskStatusQueued || (result.Attempt() >= 0 && cur.RetryCount != result.Attempt()) {
		return nil, err
	}

	log.Warn().
		Str("task_exec_id", result.TaskExecID).
		Str("workflow_exec_id", result.WorkflowExecID).
		Int("attempt", cur.RetryCount).
		Msg("result for a task whose pickup was not recorded; marking it running first")
	started := result.StartedAt
	if _, err := o.store.TransitionTask(ctx, result.TaskExecID, cur.RetryCount, models.TaskStatusRunning, persistence.TaskPatch{
		WorkerID:  result.WorkerID,
		StartedAt: &started,
	}); err != nil {
		return nil, err
	}
	return o.store.TransitionTask(ctx, result.TaskExecID, result.Attempt(), to, p)
}

// dropStaleResult turns an ErrConflict from a result's transition into a
// logged no-op: the row is not running this attempt, so the result is a
// redelivery or belongs to an attempt that has moved on. Applying it would
// double-count the task. Any other error is returned.
func dropStaleResult(result *models.TaskResult, to models.TaskStatus, err error) error {
	if !errors.Is(err, persistence.ErrConflict) {
		return fmt.Errorf("persisting task %s: %w", to, err)
	}
	log.Warn().Err(err).
		Str("task_exec_id", result.TaskExecID).
		Str("workflow_exec_id", result.WorkflowExecID).
		Int("attempt", result.Attempt()).
		Msg("dropping stale task result")
	return nil
}

// releaseSlot frees one of the execution's concurrency slots. Only an
// accepted result releases one, so a dropped duplicate cannot free a slot
// that another task still holds.
func (o *Orchestrator) releaseSlot(workflowExecID string) {
	sem := o.getSemaphore(workflowExecID)
	if sem == nil {
		return
	}
	select {
	case <-sem:
	default:
	}
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
		Msg("result for non-active workflow — checking DB")

	exec, err := o.store.GetWorkflowExecution(ctx, result.WorkflowExecID)
	if err != nil {
		return fmt.Errorf("reloading workflow execution: %w", err)
	}

	if exec.Status == models.WorkflowStatusCompleted || exec.Status == models.WorkflowStatusFailed {
		return nil
	}
	log.Info().Str("exec_id", exec.ID).Msg("workflow state reloaded from DB")
	return nil
}

// resolveArtifactsIn matches each ArtifactRef spec against the ResolvedArtifacts
// produced by dependency tasks. For each path spec, it searches all completed
// dependency tasks' ArtifactsOut for a matching path and returns the resolved key.
func (o *Orchestrator) resolveArtifactsIn(
	execCtx *ExecutionContext,
	taskDefID string,
	specs []models.ArtifactRef,
) []models.ResolvedArtifact {
	if len(specs) == 0 {
		return nil
	}

	node := execCtx.Graph.Nodes[taskDefID]
	if node == nil {
		return nil
	}

	// Collect all artifacts produced by direct and transitive dependencies
	depArtifacts := make(map[string]models.ResolvedArtifact) // path → resolved
	for _, dep := range node.Dependencies {
		depExec := execCtx.TaskMap[dep.Task.ID]
		if depExec == nil {
			continue
		}
		for _, art := range depExec.ArtifactsOut {
			depArtifacts[art.Path] = art
		}
	}

	var resolved []models.ResolvedArtifact
	for _, spec := range specs {
		if art, ok := depArtifacts[spec.Path]; ok {
			resolved = append(resolved, art)
		} else {
			log.Warn().
				Str("task_def_id", taskDefID).
				Str("artifact_path", spec.Path).
				Msg("artifact input not found among dependency outputs — will be missing in container")
		}
	}
	return resolved
}

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

// goSafe runs fn on a new goroutine via runSafe. Every per-execution
// goroutine goes through it so one bad execution cannot crash-loop the
// process. A recovered panic leaves that execution stalled until the timeout
// reaper (plan row R17) exists; nothing re-drives it before then.
func goSafe(name, execID string, fn func()) {
	go runSafe(name, execID, fn)
}

// runSafe runs fn and logs instead of propagating a panic, so a bug in one
// execution's goroutine cannot take down every other execution with the
// process. Whatever state fn left half-updated stays that way.
func runSafe(name, execID string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("goroutine", name).
				Str("exec_id", execID).
				Str("panic", fmt.Sprint(r)).
				Bytes("stack", debug.Stack()).
				Msg("recovered panic in execution goroutine")
		}
	}()
	fn()
}
