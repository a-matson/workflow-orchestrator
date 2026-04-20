package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// ListStaleRunningTasks returns tasks that have been in "running" state
// longer than maxAge without completing — indicating a possible dead worker.
func (s *Store) ListStaleRunningTasks(ctx context.Context, maxAge time.Duration) ([]*models.TaskExecution, error) {
	cutoff := time.Now().Add(-maxAge)

	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_exec_id, task_definition_id, task_name, task_type, status, 
			retry_count, max_retries, worker_id, queued_at, started_at, completed_at, next_retry_at, output, error, logs, metadata, created_at, updated_at, artifacts_in, artifacts_out
		FROM task_executions
		WHERE status = 'running'
		  AND started_at < $1
		ORDER BY started_at ASC
		LIMIT 100
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("querying stale tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*models.TaskExecution
	for rows.Next() {
		task, err := scanTaskExecution(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// GetExecutionByWorkflowAndStatus returns the most recent execution of a workflow
// in a given status — useful for idempotent trigger logic.
func (s *Store) GetExecutionByWorkflowAndStatus(ctx context.Context, workflowID string, status models.WorkflowStatus) (*models.WorkflowExecution, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, workflow_name, status, trigger_payload, metadata,
		       started_at, completed_at, error, created_at, updated_at
		FROM workflow_executions
		WHERE workflow_id = $1 AND status = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, workflowID, status)

	exec := &models.WorkflowExecution{}
	var payloadJSON, metaJSON []byte
	var errorStr *string // Pointer to handle NULL
	err := row.Scan(&exec.ID, &exec.WorkflowID, &exec.WorkflowName, &exec.Status,
		&payloadJSON, &metaJSON, &exec.StartedAt, &exec.CompletedAt,
		&errorStr, &exec.CreatedAt, &exec.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if errorStr != nil {
		exec.Error = *errorStr
	}

	return exec, nil
}

// CountActiveExecutions returns the number of running/pending executions for a workflow.
func (s *Store) CountActiveExecutions(ctx context.Context, workflowID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM workflow_executions
		WHERE workflow_id = $1 AND status IN ('running', 'pending')
	`, workflowID).Scan(&count)
	return count, err
}
