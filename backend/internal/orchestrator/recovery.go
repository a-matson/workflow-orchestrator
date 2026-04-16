package orchestrator

import (
	"context"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/dag"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/rs/zerolog/log"
)

// RecoverInFlightExecutions reloads all RUNNING workflow executions from PostgreSQL
// on startup so the orchestrator can resume dispatching after a crash/restart.
// This implements the idempotent crash-recovery guarantee.
func (o *Orchestrator) RecoverInFlightExecutions(ctx context.Context) error {
	log.Info().Msg("scanning for in-flight workflow executions to recover...")

	execs, err := o.store.ListWorkflowExecutions(ctx, 200, 0)
	if err != nil {
		return err
	}

	recovered := 0
	for _, exec := range execs {
		if exec.Status != models.WorkflowStatusRunning && exec.Status != models.WorkflowStatusPending {
			continue
		}
		if err := o.recoverExecution(ctx, exec); err != nil {
			log.Error().Err(err).Str("exec_id", exec.ID).Msg("failed to recover execution")
			continue
		}
		recovered++
	}

	log.Info().Int("recovered", recovered).Msg("crash recovery complete")
	return nil
}

func (o *Orchestrator) recoverExecution(ctx context.Context, exec *models.WorkflowExecution) error {
	// Reload full execution with tasks
	fullExec, err := o.store.GetWorkflowExecution(ctx, exec.ID)
	if err != nil {
		return err
	}

	def, err := o.store.GetWorkflowDefinition(ctx, exec.WorkflowID)
	if err != nil {
		return err
	}

	graph, err := dag.Parse(def)
	if err != nil {
		return err
	}

	// Rebuild task map
	taskMap := make(map[string]*models.TaskExecution)
	completed := make(map[string]bool)
	running := make(map[string]bool)
	queued := make(map[string]bool)
	failed := make(map[string]bool)

	for _, task := range fullExec.Tasks {
		taskMap[task.TaskDefinitionID] = task
		switch task.Status {
		case models.TaskStatusCompleted:
			completed[task.TaskDefinitionID] = true

		case models.TaskStatusRunning:
			log.Info().
				Str("exec_id", exec.ID).
				Str("task_id", task.TaskDefinitionID).
				Str("worker_id", task.WorkerID).
				Msg("recovering in-flight task — checking worker liveness")

			// Check whether the worker that owned this task is still alive.
			// IsWorkerAlive looks for a Redis heartbeat key set by the worker every 15s with a 45s TTL.
			alive, liveErr := o.redis.IsWorkerAlive(ctx, task.WorkerID)
			if liveErr != nil {
				log.Warn().Err(liveErr).Str("worker_id", task.WorkerID).
					Msg("could not check worker liveness — assuming dead, resetting task")
				alive = false
			}

			if alive {
				// Worker is still alive: leave in running state, it will publish a result.
				log.Info().Str("task_id", task.TaskDefinitionID).Str("worker_id", task.WorkerID).
					Msg("worker still alive — leaving task in running state")
				running[task.TaskDefinitionID] = true
			} else {
				// Worker is dead: reset to queued so the dispatcher re-sends it.
				log.Warn().Str("task_id", task.TaskDefinitionID).Str("worker_id", task.WorkerID).
					Msg("worker heartbeat expired — resetting task to queued")
				task.Status = models.TaskStatusQueued
				task.WorkerID = ""
				task.StartedAt = nil
				task.UpdatedAt = time.Now()
				o.store.UpdateTaskExecution(ctx, task) // nolint:errcheck // best-effort on recovery
				queued[task.TaskDefinitionID] = true
			}

		case models.TaskStatusQueued, models.TaskStatusRetrying:
			queued[task.TaskDefinitionID] = true

		case models.TaskStatusFailed, models.TaskStatusDeadLetter:
			failed[task.TaskDefinitionID] = true

		case models.TaskStatusPending, models.TaskStatusSkipped:
			// Do nothing, handled by regular DAG progression
		}
	}

	// Rebuild semaphore
	maxParallel := def.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 10
	}
	o.semMu.Lock()
	o.semaphores[exec.ID] = make(chan struct{}, maxParallel)
	o.semMu.Unlock()

	// Restore execution context
	execCtx := &ExecutionContext{
		Execution:  fullExec,
		Definition: def,
		Graph:      graph,
		TaskMap:    taskMap,
		Completed:  completed,
		Running:    running,
		Queued:     queued,
		Failed:     failed,
	}

	o.activeMu.Lock()
	o.active[exec.ID] = execCtx
	o.activeMu.Unlock()

	log.Info().
		Str("exec_id", exec.ID).
		Str("workflow", def.Name).
		Int("completed", len(completed)).
		Int("queued", len(queued)).
		Int("running", len(running)).
		Msg("execution recovered — resuming dispatch")

	go o.dispatchReadyTasks(ctx, execCtx)
	return nil
}
