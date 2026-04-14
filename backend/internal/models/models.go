package models

import (
	"encoding/json"
	"time"
)

// TaskStatus represents the lifecycle state of a task
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusQueued     TaskStatus = "queued"
	TaskStatusRunning    TaskStatus = "running"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusRetrying   TaskStatus = "retrying"
	TaskStatusSkipped    TaskStatus = "skipped"
	TaskStatusDeadLetter TaskStatus = "dead_letter"
)

// WorkflowStatus represents the lifecycle state of a workflow execution
type WorkflowStatus string

const (
	WorkflowStatusPending   WorkflowStatus = "pending"
	WorkflowStatusRunning   WorkflowStatus = "running"
	WorkflowStatusCompleted WorkflowStatus = "completed"
	WorkflowStatusFailed    WorkflowStatus = "failed"
	WorkflowStatusCancelled WorkflowStatus = "cancelled"
	WorkflowStatusPaused    WorkflowStatus = "paused"
)

// RetryPolicy defines retry behavior for tasks
type RetryPolicy struct {
	MaxRetries      int           `json:"max_retries"`
	InitialDelay    time.Duration `json:"initial_delay"`
	MaxDelay        time.Duration `json:"max_delay"`
	BackoffMultiple float64       `json:"backoff_multiplier"`
	Jitter          bool          `json:"jitter"`
}

// TaskDefinition defines a single task within a workflow DAG
type TaskDefinition struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Dependencies []string          `json:"dependencies"`
	Config       map[string]any    `json:"config"`
	RetryPolicy  *RetryPolicy      `json:"retry_policy,omitempty"`
	Timeout      time.Duration     `json:"timeout"`
	MaxParallel  int               `json:"max_parallel,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// WorkflowDefinition is the DAG specification
type WorkflowDefinition struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Tasks       []TaskDefinition  `json:"tasks"`
	GlobalRetry *RetryPolicy      `json:"global_retry,omitempty"`
	MaxParallel int               `json:"max_parallel"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// WorkflowExecution is a runtime instance of a WorkflowDefinition
type WorkflowExecution struct {
	ID             string            `json:"id"`
	WorkflowID     string            `json:"workflow_id"`
	WorkflowName   string            `json:"workflow_name"`
	Status         WorkflowStatus    `json:"status"`
	Tasks          []*TaskExecution  `json:"tasks"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	CompletedAt    *time.Time        `json:"completed_at,omitempty"`
	TriggerPayload map[string]any    `json:"trigger_payload,omitempty"`
	Error          string            `json:"error,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// TaskExecution tracks the runtime state of a single task
type TaskExecution struct {
	ID               string            `json:"id"`
	WorkflowExecID   string            `json:"workflow_exec_id"`
	TaskDefinitionID string            `json:"task_definition_id"`
	TaskName         string            `json:"task_name"`
	TaskType         string            `json:"task_type"`
	Status           TaskStatus        `json:"status"`
	RetryCount       int               `json:"retry_count"`
	MaxRetries       int               `json:"max_retries"`
	WorkerID         string            `json:"worker_id,omitempty"`
	QueuedAt         *time.Time        `json:"queued_at,omitempty"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	CompletedAt      *time.Time        `json:"completed_at,omitempty"`
	NextRetryAt      *time.Time        `json:"next_retry_at,omitempty"`
	Output           json.RawMessage   `json:"output,omitempty"`
	Error            string            `json:"error,omitempty"`
	Logs             []LogEntry        `json:"logs,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Duration         *time.Duration    `json:"duration,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// LogEntry represents a single log line from a task execution
type LogEntry struct {
	Timestamp time.Time      `json:"timestamp"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// TaskMessage is what gets enqueued in Redis for workers
type TaskMessage struct {
	TaskExecID       string         `json:"task_exec_id"`
	WorkflowExecID   string         `json:"workflow_exec_id"`
	WorkflowID       string         `json:"workflow_id"`
	TaskDefinitionID string         `json:"task_def_id"`
	TaskName         string         `json:"task_name"`
	TaskType         string         `json:"task_type"`
	Config           map[string]any `json:"config"`
	RetryCount       int            `json:"retry_count"`
	MaxRetries       int            `json:"max_retries"`
	Timeout          time.Duration  `json:"timeout"`
	EnqueuedAt       time.Time      `json:"enqueued_at"`
	IdempotencyKey   string         `json:"idempotency_key"`
}

// TaskResult is what workers publish back
type TaskResult struct {
	TaskExecID     string          `json:"task_exec_id"`
	WorkflowExecID string          `json:"workflow_exec_id"`
	WorkerID       string          `json:"worker_id"`
	Success        bool            `json:"success"`
	Output         json.RawMessage `json:"output,omitempty"`
	Error          string          `json:"error,omitempty"`
	Logs           []LogEntry      `json:"logs,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	CompletedAt    time.Time       `json:"completed_at"`
}

// WebSocketEvent is sent to connected UI clients
type WebSocketEvent struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

const (
	WSEventWorkflowStarted   = "workflow.started"
	WSEventWorkflowCompleted = "workflow.completed"
	WSEventWorkflowFailed    = "workflow.failed"
	WSEventTaskQueued        = "task.queued"
	WSEventTaskStarted       = "task.started"
	WSEventTaskCompleted     = "task.completed"
	WSEventTaskFailed        = "task.failed"
	WSEventTaskRetrying      = "task.retrying"
	WSEventTaskLog           = "task.log"
	WSEventMetrics           = "metrics.update"
)
