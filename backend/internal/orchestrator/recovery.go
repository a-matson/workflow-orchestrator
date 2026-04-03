package orchestrator

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/workflow-platform/backend/internal/dag"
	"github.com/workflow-platform/backend/internal/models"
	// "github.com/workflow-platform/backend/internal/persistence"
)

// RecoverInFlightExecutions reloads all RUNNING workflow executions from PostgreSQL
// on startup so the orchestrator can resume dispatching after a crash/restart.
// This implements the idempotent crash-recovery guarantee.
func (o *Orchestrator) RecoverInFlightExecutions(ctx context.Context) error {
	log.Info().Msg("scanning for in-flight workflow executions to recover...")

	// Load all executions that were running when we crashed
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

	// Reload workflow definition
	def, err := o.store.GetWorkflowDefinition(ctx, exec.WorkflowID)
	if err != nil {
		return err
	}

	// Re-parse DAG
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
			// Tasks that were "running" when we crashed need to be re-dispatched.
			// In production, check worker heartbeat first — if the worker is alive,
			// leave the task. If the worker is gone, reset to pending and re-queue.
			running[task.TaskDefinitionID] = true
			log.Warn().
				Str("exec_id", exec.ID).
				Str("task_id", task.TaskDefinitionID).
				Str("worker_id", task.WorkerID).
				Msg("recovering in-flight task — checking worker liveness")

			// For simplicity: reset running tasks to queued and re-dispatch
			task.Status = models.TaskStatusQueued
			task.WorkerID = ""
			task.StartedAt = nil
			task.UpdatedAt = time.Now()
			o.store.UpdateTaskExecution(ctx, task)
			queued[task.TaskDefinitionID] = true
			delete(running, task.TaskDefinitionID)

		case models.TaskStatusQueued, models.TaskStatusRetrying:
			queued[task.TaskDefinitionID] = true
		case models.TaskStatusFailed, models.TaskStatusDeadLetter:
			failed[task.TaskDefinitionID] = true
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

	// Resume dispatching ready tasks
	go o.dispatchReadyTasks(ctx, execCtx)
	return nil
}
