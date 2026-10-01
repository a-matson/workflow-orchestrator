package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// ErrConflict means a transition matched no row: the row is missing, is not
// in an allowed from-status, or has moved on to another attempt.
var ErrConflict = errors.New("state transition conflict")

// ErrInvalidTransition means the target status has no allowed from-status,
// so no row could ever match. It signals a programming error, not a race.
var ErrInvalidTransition = errors.New("invalid state transition target")

// Must match the scan order of scanTaskExecution and scanWorkflowExecution.
const (
	taskColumns = `id, workflow_exec_id, task_definition_id, task_name, task_type, status,
	       retry_count, max_retries, worker_id, queued_at, started_at, completed_at,
	       next_retry_at, output, error, metadata, created_at, updated_at,
	       artifacts_in, artifacts_out`
	execColumns = `id, workflow_id, workflow_name, status, trigger_payload, metadata,
	       started_at, completed_at, error, created_at, updated_at`
)

// TaskPatch lists the columns a task transition writes besides status.
// A nil or zero field leaves its column unchanged; Logs are appended.
type TaskPatch struct {
	// ExpectWorkerID is a precondition, not a write: when non-empty, the
	// transition matches only a row whose worker_id equals it.
	ExpectWorkerID string

	WorkerID                         string
	QueuedAt, StartedAt, CompletedAt *time.Time
	NextRetryAt                      *time.Time
	TimeoutAt                        *time.Time
	RetryCount                       *int
	Error                            *string
	Output                           json.RawMessage
	Logs                             []models.LogEntry
	ArtifactsOut                     []models.ResolvedArtifact
}

// TransitionTask moves task id to `to` and applies p in one statement, only if
// the row is in one of models.TaskFrom(to), its retry_count equals attempt,
// and, when p.ExpectWorkerID is set, its worker_id equals that.
// attempt < 0 skips the retry_count guard (cancel, recovery). It returns the
// updated row, ErrConflict when no row matched, or ErrInvalidTransition when
// `to` is not a transition target.
func (s *Store) TransitionTask(ctx context.Context, id string, attempt int, to models.TaskStatus, p TaskPatch) (*models.TaskExecution, error) {
	from := models.TaskFrom(to)
	if from == nil {
		return nil, fmt.Errorf("%w: task -> %s", ErrInvalidTransition, to)
	}
	fromText := statusText(from)

	var workerID *string
	if p.WorkerID != "" {
		workerID = &p.WorkerID
	}
	logs := []byte("[]")
	if len(p.Logs) > 0 {
		b, err := json.Marshal(p.Logs)
		if err != nil {
			return nil, fmt.Errorf("marshaling logs: %w", err)
		}
		logs = b
	}
	var output []byte
	if len(p.Output) > 0 {
		output = p.Output
	}
	var artifactsOut []byte
	if p.ArtifactsOut != nil {
		b, err := json.Marshal(p.ArtifactsOut)
		if err != nil {
			return nil, fmt.Errorf("marshaling artifacts_out: %w", err)
		}
		artifactsOut = b
	}

	// One statement: the logs are inserted only if the transition matched,
	// in their order, so a dropped stale result leaves no log rows behind.
	row := s.pool.QueryRow(ctx, `
		WITH t AS (
		UPDATE task_executions SET
			status        = $2,
			worker_id     = COALESCE($4, worker_id),
			queued_at     = COALESCE($5, queued_at),
			started_at    = COALESCE($6, started_at),
			completed_at  = COALESCE($7, completed_at),
			next_retry_at = COALESCE($8, next_retry_at),
			retry_count   = COALESCE($9::int, retry_count),
			error         = COALESCE($10, error),
			output        = COALESCE($11::jsonb, output),
			artifacts_out = COALESCE($13::jsonb, artifacts_out),
			timeout_at    = COALESCE($16, timeout_at)
		WHERE id = $1 AND status = ANY($14) AND ($3::int < 0 OR retry_count = $3::int)
		  AND ($15::text = '' OR worker_id = $15::text)
		RETURNING `+taskColumns+`
		), l AS (
			INSERT INTO task_logs (task_exec_id, attempt, logged_at, level, message, fields)
			SELECT t.id, COALESCE((x.e->>'attempt')::int, 0), (x.e->>'timestamp')::timestamptz,
				x.e->>'level', x.e->>'message', x.e->'fields'
			FROM t, jsonb_array_elements($12::jsonb) WITH ORDINALITY AS x(e, n)
			ORDER BY x.n
		)
		SELECT `+taskColumns+` FROM t`,
		id, string(to), attempt, workerID, p.QueuedAt, p.StartedAt, p.CompletedAt,
		p.NextRetryAt, p.RetryCount, p.Error, output, logs, artifactsOut, fromText, p.ExpectWorkerID, p.TimeoutAt)

	task, err := scanTaskExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: task %s -> %s (attempt %d)", ErrConflict, id, to, attempt)
	}
	if err != nil {
		return nil, fmt.Errorf("transitioning task %s to %s: %w", id, to, err)
	}
	return task, nil
}

// TimedOutTask identifies a running attempt whose timeout_at has passed.
type TimedOutTask struct {
	ID, WorkflowExecID, WorkerID string
	RetryCount                   int
}

// ListTimedOutTasks returns the running tasks whose timeout_at is before now.
func (s *Store) ListTimedOutTasks(ctx context.Context, now time.Time) ([]TimedOutTask, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_exec_id, COALESCE(worker_id, ''), retry_count
		FROM task_executions WHERE status = 'running' AND timeout_at < $1
	`, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TimedOutTask, error) {
		var t TimedOutTask
		err := row.Scan(&t.ID, &t.WorkflowExecID, &t.WorkerID, &t.RetryCount)
		return t, err
	})
}

// TransitionExecution moves execution id to `to` in one statement, only if the
// row is in one of models.ExecFrom(to). Entering running stamps started_at if
// unset; entering a terminal status stamps completed_at. A non-empty errMsg
// overwrites error. The returned execution has no Tasks loaded. Errors are as
// for TransitionTask.
func (s *Store) TransitionExecution(ctx context.Context, id string, to models.WorkflowStatus, errMsg string) (*models.WorkflowExecution, error) {
	return transitionExecution(ctx, s.pool, id, to, errMsg)
}

// FinishExecution moves execution id to the terminal status `to` and cancels
// its open tasks in one transaction, returning the execution (no Tasks
// loaded) and the cancelled rows. Either both writes commit or neither does:
// a terminal execution with open tasks is never recovered, and a queued row
// under it would still be picked up and run. Errors are as for
// TransitionExecution.
func (s *Store) FinishExecution(ctx context.Context, id string, to models.WorkflowStatus, errMsg string) (*models.WorkflowExecution, []*models.TaskExecution, error) {
	if to == models.WorkflowStatusRunning {
		return nil, nil, fmt.Errorf("%w: execution %s -> %s is not terminal", ErrInvalidTransition, id, to)
	}
	var (
		exec  *models.WorkflowExecution
		tasks []*models.TaskExecution
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if exec, err = transitionExecution(ctx, tx, id, to, errMsg); err != nil {
			return err
		}
		tasks, err = cancelOpenTasks(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return exec, tasks, nil
}

// ResumeExecution reopens execution id, which must be failed or cancelled:
// it moves back to running and every task that did not complete moves back to
// pending at attempt 0, in one transaction. It returns the execution and the
// reopened tasks, or ErrConflict when id is missing or not resumable.
func (s *Store) ResumeExecution(ctx context.Context, id string) (*models.WorkflowExecution, []*models.TaskExecution, error) {
	var (
		exec  *models.WorkflowExecution
		tasks []*models.TaskExecution
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE workflow_executions SET status = $2, completed_at = NULL, error = NULL
			WHERE id = $1 AND status = ANY($3)
			RETURNING `+execColumns,
			id, string(models.WorkflowStatusRunning), statusText(models.ExecResumeFrom()))
		var err error
		exec, err = scanWorkflowExecution(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: execution %s cannot be resumed", ErrConflict, id)
		}
		if err != nil {
			return fmt.Errorf("resuming execution %s: %w", id, err)
		}
		// Attempt 0 again: the reopened task gets its full retry budget, and
		// the pickup and result guards compare against the new attempts.
		rows, err := tx.Query(ctx, `
			UPDATE task_executions SET status = $2, retry_count = 0, worker_id = NULL, queued_at = NULL,
				started_at = NULL, completed_at = NULL, next_retry_at = NULL, timeout_at = NULL, error = NULL
			WHERE workflow_exec_id = $1 AND status = ANY($3)
			RETURNING `+taskColumns,
			id, string(models.TaskStatusPending), statusText(models.TaskResumeFrom()))
		if err != nil {
			return fmt.Errorf("reopening tasks of %s: %w", id, err)
		}
		defer rows.Close()
		for rows.Next() {
			task, err := scanTaskExecution(rows)
			if err != nil {
				return fmt.Errorf("reopening tasks of %s: %w", id, err)
			}
			tasks = append(tasks, task)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	return exec, tasks, nil
}

func transitionExecution(ctx context.Context, db querier, id string, to models.WorkflowStatus, errMsg string) (*models.WorkflowExecution, error) {
	from := models.ExecFrom(to)
	if from == nil {
		return nil, fmt.Errorf("%w: execution -> %s", ErrInvalidTransition, to)
	}
	fromText := statusText(from)

	// Every valid target except running is terminal.
	terminal := to != models.WorkflowStatusRunning
	var errText *string
	if errMsg != "" {
		errText = &errMsg
	}

	row := db.QueryRow(ctx, `
		UPDATE workflow_executions SET
			status       = $2,
			started_at   = CASE WHEN $2 = 'running' THEN COALESCE(started_at, NOW()) ELSE started_at END,
			completed_at = CASE WHEN $3::bool THEN NOW() ELSE completed_at END,
			error        = COALESCE($4, error)
		WHERE id = $1 AND status = ANY($5)
		RETURNING `+execColumns,
		id, string(to), terminal, errText, fromText)

	exec, err := scanWorkflowExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: execution %s -> %s", ErrConflict, id, to)
	}
	if err != nil {
		return nil, fmt.Errorf("transitioning execution %s to %s: %w", id, to, err)
	}
	return exec, nil
}

// CancelOpenTasks moves every task of execution execID that is in one of
// models.TaskFrom(TaskStatusCancelled) to cancelled in one statement, stamps
// completed_at, and returns the rows it changed. Tasks already final are left
// alone. It does not stop a running task's container.
func (s *Store) CancelOpenTasks(ctx context.Context, execID string) ([]*models.TaskExecution, error) {
	return cancelOpenTasks(ctx, s.pool, execID)
}

func cancelOpenTasks(ctx context.Context, db querier, execID string) ([]*models.TaskExecution, error) {
	rows, err := db.Query(ctx, `
		UPDATE task_executions SET status = $2, completed_at = NOW()
		WHERE workflow_exec_id = $1 AND status = ANY($3)
		RETURNING `+taskColumns,
		execID, string(models.TaskStatusCancelled), statusText(models.TaskFrom(models.TaskStatusCancelled)))
	if err != nil {
		return nil, fmt.Errorf("cancelling open tasks of %s: %w", execID, err)
	}
	defer rows.Close()

	var tasks []*models.TaskExecution
	for rows.Next() {
		task, err := scanTaskExecution(rows)
		if err != nil {
			return nil, fmt.Errorf("cancelling open tasks of %s: %w", execID, err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cancelling open tasks of %s: %w", execID, err)
	}
	return tasks, nil
}

// statusText converts statuses for a text[] query parameter.
func statusText[S ~string](statuses []S) []string {
	out := make([]string, len(statuses))
	for i, st := range statuses {
		out[i] = string(st)
	}
	return out
}
