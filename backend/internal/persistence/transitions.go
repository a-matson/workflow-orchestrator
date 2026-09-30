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
	       next_retry_at, output, error, logs, metadata, created_at, updated_at,
	       artifacts_in, artifacts_out`
	execColumns = `id, workflow_id, workflow_name, status, trigger_payload, metadata,
	       started_at, completed_at, error, created_at, updated_at`
)

// TaskPatch lists the columns a task transition writes besides status.
// A nil or zero field leaves its column unchanged; Logs are appended.
type TaskPatch struct {
	WorkerID                         string
	QueuedAt, StartedAt, CompletedAt *time.Time
	NextRetryAt                      *time.Time
	RetryCount                       *int
	Error                            *string
	Output                           json.RawMessage
	Logs                             []models.LogEntry
	ArtifactsOut                     []models.ResolvedArtifact
}

// TransitionTask moves task id to `to` and applies p in one statement, only if
// the row is in one of models.TaskFrom(to) and its retry_count equals attempt.
// attempt < 0 skips the retry_count guard (cancel, recovery). It returns the
// updated row, ErrConflict when no row matched, or ErrInvalidTransition when
// `to` is not a transition target.
func (s *Store) TransitionTask(ctx context.Context, id string, attempt int, to models.TaskStatus, p TaskPatch) (*models.TaskExecution, error) {
	from := models.TaskFrom(to)
	if from == nil {
		return nil, fmt.Errorf("%w: task -> %s", ErrInvalidTransition, to)
	}
	fromText := make([]string, len(from))
	for i, f := range from {
		fromText[i] = string(f)
	}

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
	var artifactsOut []byte
	if p.ArtifactsOut != nil {
		b, err := json.Marshal(p.ArtifactsOut)
		if err != nil {
			return nil, fmt.Errorf("marshaling artifacts_out: %w", err)
		}
		artifactsOut = b
	}

	// The logs CASE exists because UpdateTaskExecution writes the whole row
	// and stores JSON null for a task without logs; null || array is null.
	row := s.pool.QueryRow(ctx, `
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
			logs          = (CASE WHEN jsonb_typeof(logs) = 'array' THEN logs ELSE '[]'::jsonb END) || $12::jsonb,
			artifacts_out = COALESCE($13::jsonb, artifacts_out)
		WHERE id = $1 AND status = ANY($14) AND ($3::int < 0 OR retry_count = $3::int)
		RETURNING `+taskColumns,
		id, string(to), attempt, workerID, p.QueuedAt, p.StartedAt, p.CompletedAt,
		p.NextRetryAt, p.RetryCount, p.Error, []byte(p.Output), logs, artifactsOut, fromText)

	task, err := scanTaskExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: task %s -> %s (attempt %d)", ErrConflict, id, to, attempt)
	}
	if err != nil {
		return nil, fmt.Errorf("transitioning task %s to %s: %w", id, to, err)
	}
	return task, nil
}

// TransitionExecution moves execution id to `to` in one statement, only if the
// row is in one of models.ExecFrom(to). Entering running stamps started_at if
// unset; entering a terminal status stamps completed_at. A non-empty errMsg
// overwrites error. The returned execution has no Tasks loaded. Errors are as
// for TransitionTask.
func (s *Store) TransitionExecution(ctx context.Context, id string, to models.WorkflowStatus, errMsg string) (*models.WorkflowExecution, error) {
	from := models.ExecFrom(to)
	if from == nil {
		return nil, fmt.Errorf("%w: execution -> %s", ErrInvalidTransition, to)
	}
	fromText := make([]string, len(from))
	for i, f := range from {
		fromText[i] = string(f)
	}

	// Every valid target except running is terminal.
	terminal := to != models.WorkflowStatusRunning
	var errText *string
	if errMsg != "" {
		errText = &errMsg
	}

	row := s.pool.QueryRow(ctx, `
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
