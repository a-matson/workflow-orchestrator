package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
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
	canceller   TaskCanceller
	durations   DurationObserver

	// In-memory state for active executions (keyed by workflow exec ID).
	// Lock order: activeMu and an ExecutionContext's mu are never held
	// together, in either order; keep it so, as dispatch holds ec.mu for long.
	activeMu sync.RWMutex
	active   map[string]*ExecutionContext

	metrics *Metrics
}

// ExecutionContext holds runtime state for one active workflow execution.
// mu guards the Completed/Failed maps, TaskMap, the task rows in
// Execution.Tasks, and done. dispatchReadyTasks holds it across its store
// and Redis calls so the cache, from which it counts free slots, stays
// consistent with what it queued; the store's transition is what stops a
// double queue.
// Never call any method that re-acquires this mutex while holding it.
type ExecutionContext struct {
	Execution  *models.WorkflowExecution
	Definition *models.WorkflowDefinition
	Graph      *dag.Graph
	TaskMap    map[string]*models.TaskExecution // taskDefID -> TaskExecution
	Completed  map[string]bool
	Failed     map[string]bool
	// Skipped holds tasks whose trigger rule could no longer be met.
	Skipped map[string]bool
	// done is set once the execution's terminal status is committed; a late
	// result must not change the rows its final event reported.
	done bool
	mu   sync.Mutex
}

// EventBroadcaster sends real-time updates to connected WebSocket clients
type EventBroadcaster interface {
	Broadcast(event models.WebSocketEvent)
}

// TaskCanceller stops a task's run in this process, killing its container.
// Cancelling a task with no run here is a no-op. worker.Pool implements it;
// an interface keeps the worker package from being imported here.
type TaskCanceller interface {
	Cancel(taskExecID string)
}

// SetTaskCanceller makes execution cancels and failures stop the runs of
// the tasks they close. Call it before any execution can finish.
func (o *Orchestrator) SetTaskCanceller(c TaskCanceller) {
	o.canceller = c
}

// DurationObserver records a value, such as a Prometheus histogram does.
type DurationObserver interface {
	Observe(float64)
}

// SetTaskDurationObserver makes each completed task report its run time in
// seconds to d. Call it before any result is processed.
func (o *Orchestrator) SetTaskDurationObserver(d DurationObserver) {
	o.durations = d
}

// stopRuns stops the runs of tasks, which FinishExecution just closed.
// Every row is passed: the returned rows are already cancelled, so which
// were running is not known, and the rest have no run to stop.
func (o *Orchestrator) stopRuns(tasks []*models.TaskExecution) {
	if o.canceller == nil {
		return
	}
	for _, t := range tasks {
		o.canceller.Cancel(t.ID)
	}
}

// Metrics tracks orchestrator performance
type Metrics struct {
	WorkflowsStarted   int64
	WorkflowsCompleted int64
	WorkflowsFailed    int64
	WorkflowsCancelled int64
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
		maxRetries := retry.EffectivePolicy(taskDef.RetryPolicy, def.GlobalRetry).MaxRetries

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

	if err := o.store.CreateExecutionWithTasks(ctx, exec, def, exec.Tasks); err != nil {
		return nil, fmt.Errorf("persisting execution and tasks: %w", err)
	}

	execCtx := &ExecutionContext{
		Execution:  exec,
		Definition: def,
		Graph:      graph,
		TaskMap:    taskMap,
		Completed:  make(map[string]bool),
		Failed:     make(map[string]bool),
		Skipped:    make(map[string]bool),
	}

	// Taken before the context is shared: dispatch starts writing the live
	// rows as soon as it is registered.
	snap := execCtx.snapshot()

	// Register in active map
	o.activeMu.Lock()
	o.active[exec.ID] = execCtx
	o.activeMu.Unlock()

	o.metrics.mu.Lock()
	o.metrics.WorkflowsStarted++
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventWorkflowStarted, Payload: snap})

	log.Info().Str("exec_id", exec.ID).Str("workflow", def.Name).Msg("workflow execution started")

	// Dispatch first wave of tasks (those with no dependencies)
	dispatchCtx := context.WithoutCancel(ctx)
	runSafe("dispatch", exec.ID, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })

	return snap, nil
}

// dispatchReadyTasks queues and enqueues ready tasks of execCtx, up to MaxParallel open at once. Each
// row is moved to queued before its message exists, so a worker can never
// hold a message for a row the store does not yet show as queued (REL-6).
// At most MaxParallel of the execution's tasks are queued or running at
// once. The limit is counted from the task rows rather than tracked in a
// separate counter, so duplicate results, retries and recovery cannot make
// the two drift apart (REL-12).
// It holds execCtx.mu throughout; see ExecutionContext.
func (o *Orchestrator) dispatchReadyTasks(ctx context.Context, execCtx *ExecutionContext) {
	execCtx.mu.Lock()
	defer execCtx.mu.Unlock()
	if execCtx.done {
		return
	}

	limit := execCtx.Definition.MaxParallel
	if limit <= 0 {
		limit = defaultMaxParallel
	}
	for _, taskDefID := range execCtx.readyTasks(time.Now()) {
		// Recounted per task: a dispatch that fails can still cache a row
		// that is queued or running, when its re-read finds another write.
		if execCtx.openTasks() >= limit {
			log.Debug().Str("task_id", taskDefID).Msg("concurrency limit reached, task deferred")
			return
		}
		o.dispatchTask(ctx, execCtx, taskDefID)
	}
}

// defaultMaxParallel applies when a definition leaves max_parallel unset.
const defaultMaxParallel = 10

// openTasks counts the tasks holding a dispatch: queued or running.
// Callers hold c.mu.
func (c *ExecutionContext) openTasks() int {
	n := 0
	for _, task := range c.TaskMap {
		if task.Status == models.TaskStatusQueued || task.Status == models.TaskStatusRunning {
			n++
		}
	}
	return n
}

// dispatchTask queues and enqueues one ready task. Callers hold execCtx.mu.
func (o *Orchestrator) dispatchTask(ctx context.Context, execCtx *ExecutionContext, taskDefID string) {
	taskDef := execCtx.Graph.Nodes[taskDefID].Task
	cached := execCtx.TaskMap[taskDefID]
	queuedAt := time.Now()
	taskExec, err := o.store.TransitionTask(ctx, cached.ID, cached.RetryCount,
		models.TaskStatusQueued, persistence.TaskPatch{QueuedAt: &queuedAt})
	if err != nil {
		// The write may have committed although the client saw an error (a
		// connection reset after COMMIT). Re-read, or the stale cache keeps
		// the task ready and every tick fails the same transition.
		cur, getErr := o.store.GetTaskExecution(ctx, cached.ID)
		if getErr != nil {
			log.Error().Err(err).AnErr("reread_err", getErr).Str("task_exec_id", cached.ID).Msg("failed to queue task")
			return
		}
		if cur.Status != models.TaskStatusQueued || cur.RetryCount != cached.RetryCount {
			execCtx.cacheTask(cur)
			log.Warn().Err(err).Str("task_exec_id", cached.ID).Str("status", string(cur.Status)).
				Msg("task not queued; cached its stored row instead")
			return
		}
		taskExec = cur
	}
	execCtx.cacheTask(taskExec)

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
		Timeout:          effectiveTimeout(taskDef.Timeout),
		EnqueuedAt:       queuedAt,
		Container:        taskDef.Container,
		ArtifactsIn:      o.resolveArtifactsIn(execCtx, taskDefID, taskDef.ArtifactsIn),
		ArtifactsOut:     taskDef.ArtifactsOut,
	}
	if err := o.redis.EnqueueTask(ctx, msg); err != nil {
		log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("failed to enqueue task")
		// Back to pending, retries included: queued -> retrying is not a
		// transition, and a pending row whose dependencies completed is
		// ready again at the next dispatch with its retry_count kept.
		// Detached: DispatchDue passes the poller's context, and a shutdown
		// that cancels it between the queued write and the push would
		// otherwise fail the rollback too and strand the row across the
		// restart.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		back, err := o.store.TransitionTask(rbCtx, taskExec.ID, taskExec.RetryCount, models.TaskStatusPending, persistence.TaskPatch{})
		if err != nil {
			// Shortcut: the row stays queued with no message until the next
			// restart. The timeout reaper reads running rows only, as a long
			// queue wait cannot be told from a lost message by age alone.
			log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("task left queued without a message")
			return
		}
		execCtx.cacheTask(back)
		return
	}

	o.metrics.mu.Lock()
	o.metrics.TasksDispatched++
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(taskEvent(models.WSEventTaskQueued, taskExec))
	log.Info().Str("task_exec_id", taskExec.ID).Str("task_name", taskDef.Name).Msg("task dispatched")
}

// readyTasks lists, in definition order, the tasks dispatch may queue at
// now: pending tasks whose dependencies have all completed, and retrying
// tasks whose retry is due. The order decides which tasks get the free slots.
// A retrying task is ready on its own clock, never because a sibling
// finished (REL-5). Callers hold c.mu.
func (c *ExecutionContext) readyTasks(now time.Time) []string {
	var ready []string
	for _, def := range c.Definition.Tasks {
		id, node := def.ID, c.Graph.Nodes[def.ID]
		switch task := c.TaskMap[id]; task.Status {
		case models.TaskStatusRetrying:
			if task.NextRetryAt == nil || !task.NextRetryAt.After(now) {
				ready = append(ready, id)
			}
		case models.TaskStatusPending:
			if c.ruleMet(def.TriggerRule, node.Dependencies) {
				ready = append(ready, id)
			}
		default:
			// Queued and running tasks already hold a dispatch; the rest are final.
		}
	}
	return ready
}

// ruleMet reports whether a pending task with rule and deps may run now.
// Callers hold c.mu.
func (c *ExecutionContext) ruleMet(rule models.TriggerRule, deps []*dag.Node) bool {
	switch rule {
	case models.TriggerRuleAllDone:
		return !slices.ContainsFunc(deps, func(d *dag.Node) bool { return !c.finished(d.Task.ID) })
	case models.TriggerRuleOneFailed:
		return slices.ContainsFunc(deps, func(d *dag.Node) bool { return c.Failed[d.Task.ID] })
	default:
		return !slices.ContainsFunc(deps, func(d *dag.Node) bool { return !c.Completed[d.Task.ID] })
	}
}

// ruleLost reports whether a pending task with rule and deps can no longer
// run, so it is skipped rather than left pending. Callers hold c.mu.
func (c *ExecutionContext) ruleLost(rule models.TriggerRule, deps []*dag.Node) bool {
	switch rule {
	case models.TriggerRuleAllDone:
		return false // runs whenever its dependencies end
	case models.TriggerRuleOneFailed:
		allDone := !slices.ContainsFunc(deps, func(d *dag.Node) bool { return !c.finished(d.Task.ID) })
		return allDone && !slices.ContainsFunc(deps, func(d *dag.Node) bool { return c.Failed[d.Task.ID] })
	default:
		return slices.ContainsFunc(deps, func(d *dag.Node) bool { return c.Failed[d.Task.ID] || c.Skipped[d.Task.ID] })
	}
}

func (c *ExecutionContext) finished(id string) bool {
	return c.Completed[id] || c.Failed[id] || c.Skipped[id]
}

// allFinished reports whether every task ended. Callers hold c.mu.
func (c *ExecutionContext) allFinished() bool {
	for _, def := range c.Definition.Tasks {
		if !c.finished(def.ID) {
			return false
		}
	}
	return true
}

// skipLost moves every pending task whose trigger rule can no longer be met
// to skipped, repeating until none is left, since a skip can doom the tasks
// after it. Callers hold c.mu.
func (o *Orchestrator) skipLost(ctx context.Context, c *ExecutionContext) {
	for changed := true; changed; {
		changed = false
		for _, def := range c.Definition.Tasks {
			task := c.TaskMap[def.ID]
			if task.Status != models.TaskStatusPending || !c.ruleLost(def.TriggerRule, c.Graph.Nodes[def.ID].Dependencies) {
				continue
			}
			now := time.Now()
			row, err := o.store.TransitionTask(ctx, task.ID, task.RetryCount, models.TaskStatusSkipped, persistence.TaskPatch{CompletedAt: &now})
			if err != nil {
				log.Error().Err(err).Str("task_exec_id", task.ID).Msg("could not skip task whose trigger rule can no longer be met")
				continue
			}
			c.cacheTask(row)
			c.Skipped[def.ID] = true
			changed = true
			o.broadcaster.Broadcast(taskEvent(models.WSEventTaskSkipped, row))
		}
	}
}

// advance is how a workflow with trigger rules moves on after a task ends:
// skip what can no longer run, dispatch what can, and finish the execution
// once every task has ended, failed if any task did.
func (o *Orchestrator) advance(ctx context.Context, execCtx *ExecutionContext) error {
	execCtx.mu.Lock()
	if execCtx.done {
		execCtx.mu.Unlock()
		return nil
	}
	o.skipLost(ctx, execCtx)
	done, failed := execCtx.allFinished(), len(execCtx.Failed) > 0
	execCtx.mu.Unlock()
	if done {
		return o.completeWorkflow(ctx, execCtx, failed)
	}
	dispatchCtx := context.WithoutCancel(ctx)
	runSafe("dispatch", execCtx.Execution.ID, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })
	return nil
}

// DispatchDue runs dispatch for every active execution. Retries become due
// with time rather than on a result, so the retry poller drives them through
// here; the same pass re-drives a task rolled back to pending after a failed
// enqueue.
// DefaultTaskTimeout bounds a task that sets no timeout of its own. Without
// one, a lost result would hold the task running until the next restart.
const DefaultTaskTimeout = time.Hour

// reapGrace is how long past its timeout a running task gets for the worker's
// own failure result (container kill, publish) before the reaper fails it.
const reapGrace = time.Minute

func effectiveTimeout(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return DefaultTaskTimeout
}

// ReapTimedOut fails every running task whose deadline passed before now
// with no result, through the same path as a worker's failure result, so the
// retry policy applies. A late real result then finds the row moved on and
// is dropped. The task's run, if one is still alive, is stopped first.
func (o *Orchestrator) ReapTimedOut(ctx context.Context, now time.Time) {
	tasks, err := o.store.ListTimedOutTasks(ctx, now)
	if err != nil {
		log.Error().Err(err).Msg("timeout reaper: could not list timed-out tasks")
		return
	}
	for _, t := range tasks {
		if o.canceller != nil {
			o.canceller.Cancel(t.ID)
		}
		attempt := t.RetryCount
		result := &models.TaskResult{
			TaskExecID:     t.ID,
			WorkflowExecID: t.WorkflowExecID,
			WorkerID:       t.WorkerID,
			RetryCount:     &attempt,
			Error:          "task timed out: no result arrived before its deadline",
			CompletedAt:    now,
		}
		log.Warn().Str("task_exec_id", t.ID).Str("exec_id", t.WorkflowExecID).Msg("timeout reaper: failing task with no result")
		if err := o.ProcessResult(ctx, result); err != nil {
			log.Error().Err(err).Str("task_exec_id", t.ID).Msg("timeout reaper: could not fail task")
		}
	}
}

func (o *Orchestrator) DispatchDue(ctx context.Context) {
	o.activeMu.RLock()
	execs := make([]*ExecutionContext, 0, len(o.active))
	for _, ec := range o.active {
		execs = append(execs, ec)
	}
	o.activeMu.RUnlock()
	for _, ec := range execs {
		if ec.Definition.UsesTriggerRules() {
			// Also skips and finishes, which a crash between a task ending and
			// its advance would otherwise leave undone.
			runSafe("advance", ec.Execution.ID, func() {
				if err := o.advance(ctx, ec); err != nil {
					log.Error().Err(err).Str("exec_id", ec.Execution.ID).Msg("could not advance execution")
				}
			})
			continue
		}
		runSafe("dispatch", ec.Execution.ID, func() { o.dispatchReadyTasks(ctx, ec) })
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

// MarkTaskRunning records workerID's pickup of attempt of task taskExecID.
// An error matching persistence.ErrConflict means the row is not queued at
// that attempt (a duplicate, a stale attempt, or a rolled-back dispatch), and
// the worker must drop the message instead of running it. Without a result
// within timeout (DefaultTaskTimeout when 0) plus reapGrace, ReapTimedOut
// fails the attempt.
func (o *Orchestrator) MarkTaskRunning(ctx context.Context, taskExecID, workerID string, attempt int, timeout time.Duration) error {
	now := time.Now()
	timeoutAt := now.Add(effectiveTimeout(timeout) + reapGrace)
	taskExec, err := o.store.TransitionTask(ctx, taskExecID, attempt, models.TaskStatusRunning, persistence.TaskPatch{
		WorkerID:  workerID,
		StartedAt: &now,
		TimeoutAt: &timeoutAt,
	})
	if err != nil {
		return err
	}

	o.activeMu.RLock()
	execCtx, ok := o.active[taskExec.WorkflowExecID]
	o.activeMu.RUnlock()
	if ok {
		execCtx.mu.Lock()
		execCtx.cacheTask(taskExec)
		execCtx.mu.Unlock()
	}

	o.broadcaster.Broadcast(taskEvent(models.WSEventTaskStarted, taskExec))
	return nil
}

func (o *Orchestrator) ProcessResult(ctx context.Context, result *models.TaskResult) error {
	execCtx, err := o.activeExecution(ctx, result.WorkflowExecID)
	if err != nil {
		return err
	}
	if execCtx == nil {
		log.Debug().
			Str("task_exec_id", result.TaskExecID).
			Str("workflow_exec_id", result.WorkflowExecID).
			Msg("dropping result for a final execution")
		return nil
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
	if o.durations != nil {
		o.durations.Observe(dur.Seconds())
	}

	// Update in-memory state — acquire lock, update maps, release, then act
	execCtx.mu.Lock()
	execCtx.cacheTask(taskExec)
	execCtx.Completed[taskDefID] = true
	totalTasks := len(execCtx.Graph.Nodes)
	completedCount := len(execCtx.Completed)
	execCtx.mu.Unlock()
	// Lock is now released — safe to call other methods

	o.metrics.mu.Lock()
	o.metrics.TasksCompleted++
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(taskEvent(models.WSEventTaskCompleted, taskExec))
	log.Info().Str("task_exec_id", taskExec.ID).Str("task_name", taskExec.TaskName).Msg("task completed")

	if execCtx.Definition.UsesTriggerRules() {
		return o.advance(ctx, execCtx)
	}
	// Check completion and dispatch next wave without holding any lock
	if completedCount == totalTasks {
		return o.completeWorkflow(ctx, execCtx, false)
	}

	// Dispatch next wave of now-unblocked tasks
	dispatchCtx := context.WithoutCancel(ctx)
	runSafe("dispatch", execCtx.Execution.ID, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })
	return nil
}

func (o *Orchestrator) handleTaskFailure(ctx context.Context, execCtx *ExecutionContext, taskExec *models.TaskExecution, taskDefID string, result *models.TaskResult) error {
	now := time.Now()

	var taskPolicy *models.RetryPolicy
	if node, ok := execCtx.Graph.Nodes[taskDefID]; ok {
		taskPolicy = node.Task.RetryPolicy
	}
	policy := retry.EffectivePolicy(taskPolicy, execCtx.Definition.GlobalRetry)

	patch := persistence.TaskPatch{
		WorkerID:    result.WorkerID,
		CompletedAt: &now,
		Error:       &result.Error,
		Logs:        result.Logs,
	}

	if o.retryMgr.ShouldRetry(taskExec, policy) {
		nextAttempt := taskExec.RetryCount + 1
		delay := o.retryMgr.NextRetryDelay(taskExec.RetryCount, policy)
		nextRetryAt := now.Add(delay)
		patch.RetryCount = &nextAttempt
		patch.NextRetryAt = &nextRetryAt

		taskExec, err := o.transitionResult(ctx, result, models.TaskStatusRetrying, patch)
		if err != nil {
			return dropStaleResult(result, models.TaskStatusRetrying, err)
		}
		log.Info().
			Str("task_exec_id", taskExec.ID).
			Str("task_name", taskExec.TaskName).
			Int("retry_count", taskExec.RetryCount).
			Int("max_retries", taskExec.MaxRetries).
			Dur("delay", delay).
			Time("next_retry_at", nextRetryAt).
			Msg("task scheduled for retry")

		// The cached row is what makes the retry due: dispatch reads its
		// next_retry_at, and the retry poller calls dispatch.
		execCtx.mu.Lock()
		execCtx.cacheTask(taskExec)
		execCtx.mu.Unlock()

		o.metrics.mu.Lock()
		o.metrics.TasksRetried++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(taskEvent(models.WSEventTaskRetrying, taskExec))

	} else {
		// Exhausted retries → dead letter
		taskExec, err := o.transitionResult(ctx, result, models.TaskStatusDeadLetter, patch)
		if err != nil {
			return dropStaleResult(result, models.TaskStatusDeadLetter, err)
		}

		if err := o.redis.SendToDeadLetter(ctx, &models.TaskMessage{
			TaskExecID:     taskExec.ID,
			WorkflowExecID: execCtx.Execution.ID,
		}, result.Error); err != nil {
			log.Error().Err(err).Str("task_exec_id", taskExec.ID).Msg("failed to record task in dead-letter queue")
		}

		execCtx.mu.Lock()
		execCtx.cacheTask(taskExec)
		execCtx.Failed[taskDefID] = true
		execCtx.mu.Unlock()

		o.metrics.mu.Lock()
		o.metrics.TasksFailed++
		o.metrics.TasksDeadLettered++
		o.metrics.mu.Unlock()

		o.broadcaster.Broadcast(taskEvent(models.WSEventTaskFailed, taskExec))

		// With trigger rules the failure flows through them (all_done and
		// one_failed tasks may still run); otherwise the workflow fails fast.
		if execCtx.Definition.UsesTriggerRules() {
			return o.advance(ctx, execCtx)
		}
		return o.completeWorkflow(ctx, execCtx, true)
	}

	return nil
}

// completeWorkflow moves the execution to completed or failed and cancels
// any task still open, as a failed execution's siblings are. A conflict means
// another terminal write, such as a cancel, got there first; that write owns
// the final event.
func (o *Orchestrator) completeWorkflow(ctx context.Context, execCtx *ExecutionContext, failed bool) error {
	status, evtType := models.WorkflowStatusCompleted, models.WSEventWorkflowCompleted
	if failed {
		status, evtType = models.WorkflowStatusFailed, models.WSEventWorkflowFailed
	}

	// Siblings are closed rather than left to finish: the execution leaves
	// the active map, so their results would be dropped as late, and
	// recovery skips failed executions. Plan row R7 may let them finish.
	row, closed, err := o.store.FinishExecution(ctx, execCtx.Execution.ID, status, "")
	if errors.Is(err, persistence.ErrConflict) {
		log.Warn().Err(err).Str("exec_id", execCtx.Execution.ID).Msg("execution already final; not overwriting it")
		return nil
	}
	if err != nil {
		return fmt.Errorf("persisting workflow completion: %w", err)
	}
	o.stopRuns(closed)

	final := o.finish(execCtx, row, closed)
	o.metrics.mu.Lock()
	if failed {
		o.metrics.WorkflowsFailed++
	} else {
		o.metrics.WorkflowsCompleted++
	}
	o.metrics.mu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{Type: evtType, Payload: final})
	log.Info().Str("exec_id", final.ID).Str("status", string(final.Status)).Msg("workflow finished")
	return nil
}

// ResumeExecution reopens a failed or cancelled execution and dispatches the
// tasks that did not complete, under the definition the run started with
// (R23's snapshot). Completed tasks keep their results and do not run again.
// It returns persistence.ErrConflict when id is missing or not resumable.
func (o *Orchestrator) ResumeExecution(ctx context.Context, id string) (*models.WorkflowExecution, error) {
	if _, _, err := o.store.ResumeExecution(ctx, id); err != nil {
		return nil, err
	}
	// The finished execution left the active map; this loads the reopened
	// rows and registers them, as for a result that reaches an unloaded run.
	execCtx, err := o.activeExecution(ctx, id)
	if err != nil {
		return nil, err
	}
	if execCtx == nil {
		return nil, fmt.Errorf("%w: execution %s was finished again before it was loaded", persistence.ErrConflict, id)
	}
	execCtx.mu.Lock()
	snap := execCtx.snapshot()
	execCtx.mu.Unlock()

	o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventWorkflowStarted, Payload: snap})
	log.Info().Str("exec_id", id).Msg("workflow execution resumed")

	dispatchCtx := context.WithoutCancel(ctx)
	if execCtx.Definition.UsesTriggerRules() {
		if err := o.advance(dispatchCtx, execCtx); err != nil {
			return nil, err
		}
		return snap, nil
	}
	runSafe("dispatch", id, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })
	return snap, nil
}

// CancelExecution moves execution id to cancelled and cancels its open tasks,
// so no further task of it is dispatched and late results are dropped. It
// returns persistence.ErrConflict when the execution is missing or already
// final. Running tasks are stopped through the TaskCanceller.
func (o *Orchestrator) CancelExecution(ctx context.Context, id string) (*models.WorkflowExecution, error) {
	row, closed, err := o.store.FinishExecution(ctx, id, models.WorkflowStatusCancelled, "")
	if err != nil {
		return nil, err
	}
	o.stopRuns(closed)

	o.activeMu.RLock()
	execCtx, ok := o.active[id]
	o.activeMu.RUnlock()
	var final *models.WorkflowExecution
	if ok {
		final = o.finish(execCtx, row, closed)
	} else {
		// Not running in this process (never recovered, or pending), so
		// there is no cache to update; the event reports the stored rows.
		if final, err = o.store.GetWorkflowExecution(ctx, id); err != nil {
			return nil, fmt.Errorf("reloading cancelled execution: %w", err)
		}
	}

	o.metrics.mu.Lock()
	o.metrics.WorkflowsCancelled++
	o.metrics.mu.Unlock()
	o.broadcaster.Broadcast(models.WebSocketEvent{Type: models.WSEventWorkflowCancelled, Payload: final})
	log.Info().Str("exec_id", id).Int("tasks_cancelled", len(closed)).Msg("workflow cancelled")
	return final, nil
}

// finish takes row, the execution's committed terminal state, and tasks into
// the cache and retires the context. Afterwards the cache matches what was
// committed, and no dispatch or late result changes it.
func (o *Orchestrator) finish(execCtx *ExecutionContext, row *models.WorkflowExecution, tasks []*models.TaskExecution) *models.WorkflowExecution {
	execCtx.mu.Lock()
	for _, task := range tasks {
		execCtx.cacheTask(task)
	}
	execCtx.Execution.Status = row.Status
	execCtx.Execution.CompletedAt = row.CompletedAt
	execCtx.Execution.UpdatedAt = row.UpdatedAt
	execCtx.Execution.Error = row.Error
	execCtx.done = true
	final := execCtx.snapshot()
	execCtx.mu.Unlock()

	o.activeMu.Lock()
	delete(o.active, execCtx.Execution.ID)
	o.activeMu.Unlock()
	return final
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

// taskEvent wraps a copy of task, as snapshot does for executions: the row
// stays in the cache, where the next transition may replace or recovery may
// rewrite it while the hub is still marshalling the event.
func taskEvent(typ string, task *models.TaskExecution) models.WebSocketEvent {
	row := *task
	return models.WebSocketEvent{Type: typ, Payload: &row}
}

// transitionResult applies a result's transition to task result.TaskExecID.
// The transition out of running also requires the row's worker to be the
// result's: after a restart re-sends an attempt, a result the killed worker
// published before the crash must not close the run a new worker holds.
// A row still queued at the result's attempt has only missed its pickup
// write, as when such a pre-crash result arrives before the re-sent message
// is picked up. It is moved to running under the result's worker and the
// transition is tried once more; the re-sent message then loses its pickup.
// Any other conflict is returned for dropStaleResult.
func (o *Orchestrator) transitionResult(ctx context.Context, result *models.TaskResult, to models.TaskStatus, p persistence.TaskPatch) (*models.TaskExecution, error) {
	p.ExpectWorkerID = result.WorkerID
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

// activeExecution returns execution id's cached context. On a miss it loads
// the execution from the store and registers it, because a result can reach
// an execution recovery skipped (past its listing cap, or after a failed
// recoverExecution), and dropping it would leave its task running forever
// (REL-9). It returns nil for a final execution: its rows
// are closed, and registering it would let dispatch run again.
func (o *Orchestrator) activeExecution(ctx context.Context, id string) (*ExecutionContext, error) {
	o.activeMu.RLock()
	execCtx, ok := o.active[id]
	o.activeMu.RUnlock()
	if ok {
		return execCtx, nil
	}

	loaded, err := o.loadExecution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading execution %s: %w", id, err)
	}
	if !isOpen(loaded.Execution.Status) {
		return nil, nil
	}

	beforeRegister()
	o.activeMu.Lock()
	// Another result for the same execution may have registered it meanwhile;
	// two contexts would each dispatch from their own copy of the rows.
	if execCtx, ok := o.active[id]; ok {
		o.activeMu.Unlock()
		return execCtx, nil
	}
	o.active[id] = loaded
	o.activeMu.Unlock()

	// A cancel that committed after the load but looked the execution up
	// before it was registered retired nothing, and the context would stay
	// active for good. Re-read after registering: a cancel committed later
	// finds the context in o.active and retires it itself.
	row, err := o.store.GetWorkflowExecution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("re-reading loaded execution %s: %w", id, err)
	}
	if !isOpen(row.Status) {
		o.finish(loaded, row, row.Tasks)
		return nil, nil
	}
	log.Info().Str("exec_id", id).Msg("execution loaded from the store for a result")
	return loaded, nil
}

// beforeRegister runs between a miss-load and its registration. It is a test
// seam: tests set it to finish the execution inside that window.
var beforeRegister = func() {}

// isOpen reports whether an execution can still change: a cancel could close
// it. It is the set of statuses recovery loads.
func isOpen(s models.WorkflowStatus) bool {
	return slices.Contains(models.ExecFrom(models.WorkflowStatusCancelled), s)
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
	active, retrying := o.activeAndRetrying()
	o.metrics.mu.Lock()
	defer o.metrics.mu.Unlock()

	return map[string]int64{
		"workflows_started":   o.metrics.WorkflowsStarted,
		"workflows_completed": o.metrics.WorkflowsCompleted,
		"workflows_failed":    o.metrics.WorkflowsFailed,
		"workflows_cancelled": o.metrics.WorkflowsCancelled,
		"tasks_dispatched":    o.metrics.TasksDispatched,
		"tasks_completed":     o.metrics.TasksCompleted,
		"tasks_failed":        o.metrics.TasksFailed,
		"tasks_retried":       o.metrics.TasksRetried,
		"tasks_dead_lettered": o.metrics.TasksDeadLettered,
		"active_workflows":    active,
		// Kept under its old name for the UI; retries now wait in the task
		// rows, not in a Redis set.
		"retry_queue_depth": retrying,
	}
}

// activeAndRetrying counts active executions and their retrying rows, both
// from one snapshot of o.active taken under activeMu. It waits on
// each execution's lock, which dispatch holds across store and Redis calls,
// so it must not hold activeMu meanwhile: a queued activeMu writer would
// then block every reader behind one slow dispatch. It must also stay
// outside metrics.mu, because dispatch takes metrics.mu under ec.mu.
func (o *Orchestrator) activeAndRetrying() (active, retrying int64) {
	o.activeMu.RLock()
	execs := make([]*ExecutionContext, 0, len(o.active))
	for _, ec := range o.active {
		execs = append(execs, ec)
	}
	o.activeMu.RUnlock()

	for _, ec := range execs {
		ec.mu.Lock()
		for _, t := range ec.TaskMap {
			if t.Status == models.TaskStatusRetrying {
				retrying++
			}
		}
		ec.mu.Unlock()
	}
	return int64(len(execs)), retrying
}

// goSafe runs fn on a new goroutine via runSafe. Every per-execution
// goroutine goes through it so one bad execution cannot crash-loop the
// process. A recovered panic leaves that execution stalled until the next
// restart: the timeout reaper only fails running rows, and a dispatch that
// panicked left its rows pending.
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
