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
	execCtx, err := o.loadExecution(ctx, exec.ID)
	if err != nil {
		return err
	}

	// The context is not shared yet, so cacheTask needs no lock here.
	for _, task := range execCtx.Execution.Tasks {
		if task.Status != models.TaskStatusQueued && task.Status != models.TaskStatusRunning {
			continue
		}
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
			continue
		}
		log.Warn().Str("exec_id", exec.ID).Str("task_id", task.TaskDefinitionID).
			Str("was", string(task.Status)).Msg("requeueing task left open by the previous process")
		execCtx.cacheTask(reset)
	}

	o.activeMu.Lock()
	o.active[exec.ID] = execCtx
	o.activeMu.Unlock()

	log.Info().
		Str("exec_id", exec.ID).
		Str("workflow", execCtx.Definition.Name).
		Int("completed", len(execCtx.Completed)).
		Msg("execution recovered — resuming dispatch")

	dispatchCtx := context.WithoutCancel(ctx)
	goSafe("dispatch", execCtx.Execution.ID, func() { o.dispatchReadyTasks(dispatchCtx, execCtx) })
	return nil
}

// loadExecution builds execution id's context from its stored rows, as they
// are: recovery requeues open tasks itself. The context is not registered.
func (o *Orchestrator) loadExecution(ctx context.Context, id string) (*ExecutionContext, error) {
	exec, err := o.store.GetWorkflowExecution(ctx, id)
	if err != nil {
		return nil, err
	}
	def, err := o.store.GetWorkflowDefinition(ctx, exec.WorkflowID)
	if err != nil {
		return nil, err
	}
	graph, err := dag.Parse(def)
	if err != nil {
		return nil, err
	}

	execCtx := &ExecutionContext{
		Execution:  exec,
		Definition: def,
		Graph:      graph,
		TaskMap:    make(map[string]*models.TaskExecution, len(exec.Tasks)),
		Completed:  make(map[string]bool),
		Failed:     make(map[string]bool),
	}
	for _, task := range exec.Tasks {
		execCtx.TaskMap[task.TaskDefinitionID] = task
		switch task.Status {
		case models.TaskStatusCompleted:
			execCtx.Completed[task.TaskDefinitionID] = true
		case models.TaskStatusFailed, models.TaskStatusDeadLetter, models.TaskStatusCancelled:
			execCtx.Failed[task.TaskDefinitionID] = true
		case models.TaskStatusPending, models.TaskStatusQueued, models.TaskStatusRunning,
			models.TaskStatusRetrying, models.TaskStatusSkipped:
			// Dispatch reads these statuses from the rows in TaskMap.
		}
	}
	return execCtx, nil
}
