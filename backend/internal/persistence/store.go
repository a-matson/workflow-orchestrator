package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// Public sentinel error
var ErrNotFound = errors.New("resource not found")

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

// Pool exposes the underlying pool for the migrator.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
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
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
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

func (s *Store) ListWorkflowDefinitions(ctx context.Context, limit, offset int) ([]*models.WorkflowDefinition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, version, tasks, max_parallel, tags, created_at, updated_at
		FROM workflow_definitions ORDER BY created_at DESC, id
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var defs []*models.WorkflowDefinition
	for rows.Next() {
		def := &models.WorkflowDefinition{}
		var tasksJSON, tagsJSON []byte
		if err := rows.Scan(&def.ID, &def.Name, &def.Description, &def.Version,
			&tasksJSON, &def.MaxParallel, &tagsJSON, &def.CreatedAt, &def.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(tasksJSON, &def.Tasks)
		_ = json.Unmarshal(tagsJSON, &def.Tags)
		defs = append(defs, def)
	}

	return defs, rows.Err()
}

// ==================== Workflow Executions ====================

func (s *Store) CreateWorkflowExecution(ctx context.Context, exec *models.WorkflowExecution) error {
	return insertExecution(ctx, s.pool, exec)
}

// CreateExecutionWithTasks inserts exec and its tasks atomically: either all
// rows exist afterwards or none do.
func (s *Store) CreateExecutionWithTasks(ctx context.Context, exec *models.WorkflowExecution, tasks []*models.TaskExecution) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertExecution(ctx, tx, exec); err != nil {
			return fmt.Errorf("inserting execution: %w", err)
		}
		for _, task := range tasks {
			if err := insertTask(ctx, tx, task); err != nil {
				return fmt.Errorf("inserting task %s: %w", task.TaskDefinitionID, err)
			}
		}
		return nil
	})
}

// execer is satisfied by *pgxpool.Pool and pgx.Tx, so inserts run the same
// SQL inside or outside a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func insertExecution(ctx context.Context, db execer, exec *models.WorkflowExecution) error {
	payloadJSON, err := json.Marshal(exec.TriggerPayload)
	if err != nil {
		return fmt.Errorf("marshaling trigger payload: %w", err)
	}
	metaJSON, err := json.Marshal(exec.Metadata)
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}

	_, err = db.Exec(ctx, `
		INSERT INTO workflow_executions
		(id, workflow_id, workflow_name, status, trigger_payload, metadata, started_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, exec.ID, exec.WorkflowID, exec.WorkflowName, exec.Status,
		payloadJSON, metaJSON, exec.StartedAt, exec.CreatedAt, exec.UpdatedAt)

	return err
}

func (s *Store) GetWorkflowExecution(ctx context.Context, id string) (*models.WorkflowExecution, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, workflow_name, status, trigger_payload, metadata, 
		       started_at, completed_at, error, created_at, updated_at
		FROM workflow_executions WHERE id = $1
	`, id)

	exec, err := scanWorkflowExecution(row)
	if err != nil {
		return nil, err
	}

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
		// Non-nil so a task-less execution serializes as [] (the UI calls .find on it).
		exec := &models.WorkflowExecution{Tasks: []*models.TaskExecution{}}
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return execs, s.attachTasks(ctx, execs)
}

// attachTasks loads the tasks of all execs in one query. Output and logs are
// left out (NULL) because they can be large and the list view does not show them.
func (s *Store) attachTasks(ctx context.Context, execs []*models.WorkflowExecution) error {
	if len(execs) == 0 {
		return nil
	}
	byID := make(map[string]*models.WorkflowExecution, len(execs))
	ids := make([]string, len(execs))
	for i, e := range execs {
		byID[e.ID] = e
		ids[i] = e.ID
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_exec_id, task_definition_id, task_name, task_type, status,
		       retry_count, max_retries, worker_id, queued_at, started_at, completed_at,
		       next_retry_at, NULL, error, NULL, metadata, created_at, updated_at,
		       artifacts_in, artifacts_out
		FROM task_executions WHERE workflow_exec_id = ANY($1) ORDER BY created_at ASC, id ASC
	`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		task, err := scanTaskExecution(rows)
		if err != nil {
			return err
		}
		e := byID[task.WorkflowExecID]
		e.Tasks = append(e.Tasks, task)
	}
	return rows.Err()
}

// ==================== Task Executions ====================

func (s *Store) CreateTaskExecution(ctx context.Context, task *models.TaskExecution) error {
	return insertTask(ctx, s.pool, task)
}

func insertTask(ctx context.Context, db execer, task *models.TaskExecution) error {
	metaJSON, err := json.Marshal(task.Metadata)
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}

	_, err = db.Exec(ctx, `
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
	artifactsInJSON, _ := json.Marshal(task.ArtifactsIn)
	artifactsOutJSON, _ := json.Marshal(task.ArtifactsOut)

	_, err := s.pool.Exec(ctx, `
		UPDATE task_executions SET
			status = $2, retry_count = $3, worker_id = $4, queued_at = $5,
			started_at = $6, completed_at = $7, next_retry_at = $8,
			output = $9, error = $10, logs = $11, updated_at = $12,
			artifacts_in = $13, artifacts_out = $14
		WHERE id = $1
	`, task.ID, task.Status, task.RetryCount, task.WorkerID,
		task.QueuedAt, task.StartedAt, task.CompletedAt, task.NextRetryAt,
		outputJSON, task.Error, logsJSON, task.UpdatedAt,
		artifactsInJSON, artifactsOutJSON)

	return err
}

func (s *Store) GetTaskExecution(ctx context.Context, id string) (*models.TaskExecution, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workflow_exec_id, task_definition_id, task_name, task_type, status,
		       retry_count, max_retries, worker_id, queued_at, started_at, completed_at,
		       next_retry_at, output, error, logs, metadata, created_at, updated_at,
		       artifacts_in, artifacts_out
		FROM task_executions WHERE id = $1
	`, id)

	return scanTaskExecution(row)
}

func (s *Store) ListTaskExecutions(ctx context.Context, workflowExecID string) ([]*models.TaskExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_exec_id, task_definition_id, task_name, task_type, status,
		       retry_count, max_retries, worker_id, queued_at, started_at, completed_at,
		       next_retry_at, output, error, logs, metadata, created_at, updated_at,
		       artifacts_in, artifacts_out
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

// ==================== Scanner helpers ====================

type scannable interface {
	Scan(dest ...any) error
}

// scanWorkflowExecution reads the columns listed in execColumns. Tasks is left nil.
func scanWorkflowExecution(row scannable) (*models.WorkflowExecution, error) {
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

	// Best effort: a malformed payload or metadata blob must not hide the
	// execution's status from callers.
	_ = json.Unmarshal(payloadJSON, &exec.TriggerPayload)
	_ = json.Unmarshal(metaJSON, &exec.Metadata)
	return exec, nil
}

func scanTaskExecution(row scannable) (*models.TaskExecution, error) {
	task := &models.TaskExecution{}
	var outputJSON, logsJSON, metaJSON []byte
	var workerID, errorStr *string
	var artifactsInJSON, artifactsOutJSON []byte

	err := row.Scan(
		&task.ID, &task.WorkflowExecID, &task.TaskDefinitionID, &task.TaskName, &task.TaskType, &task.Status,
		&task.RetryCount, &task.MaxRetries, &workerID, &task.QueuedAt, &task.StartedAt, &task.CompletedAt,
		&task.NextRetryAt, &outputJSON, &errorStr, &logsJSON, &metaJSON, &task.CreatedAt, &task.UpdatedAt,
		&artifactsInJSON, &artifactsOutJSON,
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
	if artifactsInJSON != nil {
		_ = json.Unmarshal(artifactsInJSON, &task.ArtifactsIn)
	}
	if artifactsOutJSON != nil {
		_ = json.Unmarshal(artifactsOutJSON, &task.ArtifactsOut)
	}

	return task, nil
}
