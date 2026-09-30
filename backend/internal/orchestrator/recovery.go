package orchestrator

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/dag"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// RecoverInFlightExecutions reloads every open workflow execution from
// PostgreSQL at startup and resumes dispatching it. It must run before the
// worker pool starts: it re-queues every queued or running task, which is
// only safe while no worker of this process holds one.
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

	taskMap := make(map[string]*models.TaskExecution)
	completed := make(map[string]bool)
	failed := make(map[string]bool)

	for i, task := range fullExec.Tasks {
		switch task.Status {
		case models.TaskStatusCompleted:
			completed[task.TaskDefinitionID] = true

		case models.TaskStatusQueued, models.TaskStatusRunning:
			// Workers run in this process, so none survived the restart, and
			// a queued task's message may have died in a worker's hands.
			// Back to pending, the dispatch below queues and sends it again;
			// a message still in Redis becomes a duplicate that the pickup
			// CAS drops. The dead worker's worker_id and started_at stay on
			// the pending row until the next pickup overwrites both; clearing
			// them would take a full-row write outside the transition.
			reset, err := o.store.TransitionTask(ctx, task.ID, -1, models.TaskStatusPending, persistence.TaskPatch{})
			if err != nil {
				// Skipping the execution would strand every other task in it.
				// The row keeps its slot and waits for the next restart, as a
				// stuck pickup does, until the timeout reaper (plan row R17).
				log.Error().Err(err).Str("exec_id", exec.ID).Str("task_id", task.TaskDefinitionID).
					Msg("could not requeue task left open by the previous process")
				break
			}
			log.Warn().Str("exec_id", exec.ID).Str("task_id", task.TaskDefinitionID).
				Str("was", string(task.Status)).Msg("requeueing task left open by the previous process")
			task = reset
			fullExec.Tasks[i] = reset

		case models.TaskStatusFailed, models.TaskStatusDeadLetter:
			failed[task.TaskDefinitionID] = true

		case models.TaskStatusPending, models.TaskStatusRetrying, models.TaskStatusSkipped:
			// Dispatch reads these statuses from the rows in taskMap.
		}
		taskMap[task.TaskDefinitionID] = task
	}

	execCtx := &ExecutionContext{
		Execution:  fullExec,
		Definition: def,
		Graph:      graph,
		TaskMap:    taskMap,
		Completed:  completed,
		Failed:     failed,
	}

	o.activeMu.Lock()
	o.active[exec.ID] = execCtx
	o.activeMu.Unlock()

	log.Info().
		Str("exec_id", exec.ID).
		Str("workflow", def.Name).
		Int("completed", len(completed)).
		Msg("execution recovered — resuming dispatch")

	dispatchCtx := context.WithoutCancel(ctx)
	goSafe("dispatch", execCtx.Execution.ID, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })
	return nil
}
