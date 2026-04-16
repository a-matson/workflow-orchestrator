package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store provides persistence for workflow executions and task states
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, dsn string) (*Store, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DSN: %w", err)
	}

	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

// ==================== Workflow Definitions ====================

func (s *Store) SaveWorkflowDefinition(ctx context.Context, def *models.WorkflowDefinition) error {
	tasksJSON, err := json.Marshal(def.Tasks)
	if err != nil {
		return fmt.Errorf("marshaling tasks: %w", err)
	}

	tagsJSON, err := json.Marshal(def.Tags)
	if err != nil {
		return fmt.Errorf("marshaling tags: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO workflow_definitions (id, name, description, version, tasks, max_parallel, tags, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			version = EXCLUDED.version,
			tasks = EXCLUDED.tasks,
			max_parallel = EXCLUDED.max_parallel,
			tags = EXCLUDED.tags,
			updated_at = EXCLUDED.updated_at
	`, def.ID, def.Name, def.Description, def.Version, tasksJSON, def.MaxParallel, tagsJSON, def.CreatedAt, def.UpdatedAt)

	return err
}

func (s *Store) GetWorkflowDefinition(ctx context.Context, id string) (*models.WorkflowDefinition, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, description, version, tasks, max_parallel, tags, created_at, updated_at
		FROM workflow_definitions WHERE id = $1
	`, id)

	def := &models.WorkflowDefinition{}
	var tasksJSON, tagsJSON []byte
	err := row.Scan(&def.ID, &def.Name, &def.Description, &def.Version,
		&tasksJSON, &def.MaxParallel, &tagsJSON, &def.CreatedAt, &def.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("scanning workflow definition: %w", err)
	}

	if err := json.Unmarshal(tasksJSON, &def.Tasks); err != nil {
		return nil, fmt.Errorf("unmarshaling tasks: %w", err)
	}
	if err := json.Unmarshal(tagsJSON, &def.Tags); err != nil {
		return nil, fmt.Errorf("unmarshaling tags: %w", err)
	}

	return def, nil
}

func (s *Store) ListWorkflowDefinitions(ctx context.Context) ([]*models.WorkflowDefinition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, version, max_parallel, tags, created_at, updated_at
		FROM workflow_definitions ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var defs []*models.WorkflowDefinition
	for rows.Next() {
		def := &models.WorkflowDefinition{}
		var tagsJSON []byte
		if err := rows.Scan(&def.ID, &def.Name, &def.Description, &def.Version,
			&def.MaxParallel, &tagsJSON, &def.CreatedAt, &def.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(tagsJSON, &def.Tags)
		defs = append(defs, def)
	}

	return defs, rows.Err()
}

// ==================== Workflow Executions ====================

func (s *Store) CreateWorkflowExecution(ctx context.Context, exec *models.WorkflowExecution) error {
	payloadJSON, _ := json.Marshal(exec.TriggerPayload)
	metaJSON, _ := json.Marshal(exec.Metadata)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_executions 
		(id, workflow_id, workflow_name, status, trigger_payload, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, exec.ID, exec.WorkflowID, exec.WorkflowName, exec.Status,
		payloadJSON, metaJSON, exec.CreatedAt, exec.UpdatedAt)

	return err
}

func (s *Store) UpdateWorkflowExecution(ctx context.Context, exec *models.WorkflowExecution) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_executions SET
			status = $2, started_at = $3, completed_at = $4, error = $5, updated_at = $6
		WHERE id = $1
	`, exec.ID, exec.Status, exec.StartedAt, exec.CompletedAt, exec.Error, exec.UpdatedAt)

	return err
}

func (s *Store) GetWorkflowExecution(ctx context.Context, id string) (*models.WorkflowExecution, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, workflow_name, status, trigger_payload, metadata, 
		       started_at, completed_at, error, created_at, updated_at
		FROM workflow_executions WHERE id = $1
	`, id)

	exec := &models.WorkflowExecution{}
	var payloadJSON, metaJSON []byte
	var errorStr *string
	err := row.Scan(&exec.ID, &exec.WorkflowID, &exec.WorkflowName, &exec.Status,
		&payloadJSON, &metaJSON, &exec.StartedAt, &exec.CompletedAt,
		&errorStr, &exec.CreatedAt, &exec.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if errorStr != nil {
		exec.Error = *errorStr
	}

	_ = json.Unmarshal(payloadJSON, &exec.TriggerPayload)
	_ = json.Unmarshal(metaJSON, &exec.Metadata)

	// Load task executions
	tasks, err := s.ListTaskExecutions(ctx, exec.ID)
	if err != nil {
		return nil, err
	}
	exec.Tasks = tasks

	return exec, nil
}

func (s *Store) ListWorkflowExecutions(ctx context.Context, limit, offset int) ([]*models.WorkflowExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_id, workflow_name, status, started_at, completed_at, error, created_at, updated_at
		FROM workflow_executions ORDER BY created_at DESC LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []*models.WorkflowExecution
	for rows.Next() {
		exec := &models.WorkflowExecution{}
		var errorStr *string
		if err := rows.Scan(&exec.ID, &exec.WorkflowID, &exec.WorkflowName, &exec.Status,
			&exec.StartedAt, &exec.CompletedAt, &errorStr, &exec.CreatedAt, &exec.UpdatedAt); err != nil {
			return nil, err
		}
		if errorStr != nil {
			exec.Error = *errorStr
		}
		execs = append(execs, exec)
	}

	return execs, rows.Err()
}

// ==================== Task Executions ====================

func (s *Store) CreateTaskExecution(ctx context.Context, task *models.TaskExecution) error {
	metaJSON, _ := json.Marshal(task.Metadata)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO task_executions 
		(id, workflow_exec_id, task_definition_id, task_name, task_type, status, 
		 retry_count, max_retries, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, task.ID, task.WorkflowExecID, task.TaskDefinitionID, task.TaskName, task.TaskType,
		task.Status, task.RetryCount, task.MaxRetries, metaJSON, task.CreatedAt, task.UpdatedAt)

	return err
}

func (s *Store) UpdateTaskExecution(ctx context.Context, task *models.TaskExecution) error {
	outputJSON, _ := json.Marshal(task.Output)
	logsJSON, _ := json.Marshal(task.Logs)

	_, err := s.pool.Exec(ctx, `
		UPDATE task_executions SET
			status = $2, retry_count = $3, worker_id = $4, queued_at = $5,
			started_at = $6, completed_at = $7, next_retry_at = $8,
			output = $9, error = $10, logs = $11, updated_at = $12
		WHERE id = $1
	`, task.ID, task.Status, task.RetryCount, task.WorkerID,
		task.QueuedAt, task.StartedAt, task.CompletedAt, task.NextRetryAt,
		outputJSON, task.Error, logsJSON, task.UpdatedAt)

	return err
}

func (s *Store) GetTaskExecution(ctx context.Context, id string) (*models.TaskExecution, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workflow_exec_id, task_definition_id, task_name, task_type, status,
		       retry_count, max_retries, worker_id, queued_at, started_at, completed_at,
		       next_retry_at, output, error, logs, metadata, created_at, updated_at
		FROM task_executions WHERE id = $1
	`, id)

	return scanTaskExecution(row)
}

func (s *Store) ListTaskExecutions(ctx context.Context, workflowExecID string) ([]*models.TaskExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_exec_id, task_definition_id, task_name, task_type, status,
		       retry_count, max_retries, worker_id, queued_at, started_at, completed_at,
		       next_retry_at, output, error, logs, metadata, created_at, updated_at
		FROM task_executions WHERE workflow_exec_id = $1 ORDER BY created_at ASC
	`, workflowExecID)
	if err != nil {
		return nil, err
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

// GetTasksReadyForRetry returns tasks whose retry time has passed
func (s *Store) GetTasksReadyForRetry(ctx context.Context) ([]*models.TaskExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_exec_id, task_definition_id, task_name, task_type, status,
		       retry_count, max_retries, worker_id, queued_at, started_at, completed_at,
		       next_retry_at, output, error, logs, metadata, created_at, updated_at
		FROM task_executions 
		WHERE status = 'retrying' AND next_retry_at <= NOW()
		ORDER BY next_retry_at ASC
		LIMIT 100
	`)
	if err != nil {
		return nil, err
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

// AppendTaskLog adds a log entry to a task execution (idempotent via timestamp)
func (s *Store) AppendTaskLog(ctx context.Context, taskExecID string, entry models.LogEntry) error {
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE task_executions 
		SET logs = COALESCE(logs, '[]'::jsonb) || $2::jsonb, updated_at = NOW()
		WHERE id = $1
	`, taskExecID, string(entryJSON))

	return err
}

// ==================== Scanner helpers ====================

type scannable interface {
	Scan(dest ...any) error
}

func scanTaskExecution(row scannable) (*models.TaskExecution, error) {
	task := &models.TaskExecution{}
	var outputJSON, logsJSON, metaJSON []byte
	var workerID, errorStr *string

	err := row.Scan(
		&task.ID, &task.WorkflowExecID, &task.TaskDefinitionID, &task.TaskName, &task.TaskType, &task.Status,
		&task.RetryCount, &task.MaxRetries, &workerID, &task.QueuedAt, &task.StartedAt, &task.CompletedAt,
		&task.NextRetryAt, &outputJSON, &errorStr, &logsJSON, &metaJSON, &task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if workerID != nil {
		task.WorkerID = *workerID
	}
	if errorStr != nil {
		task.Error = *errorStr
	}

	if outputJSON != nil {
		task.Output = outputJSON
	}
	if logsJSON != nil {
		_ = json.Unmarshal(logsJSON, &task.Logs)
	}
	if metaJSON != nil {
		_ = json.Unmarshal(metaJSON, &task.Metadata)
	}

	return task, nil
}
