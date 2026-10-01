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
	// TaskStatusCancelled is terminal: a task stopped because its execution
	// was cancelled, as opposed to one that failed on its own.
	TaskStatusCancelled TaskStatus = "cancelled"
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

// TriggerRule decides when a task runs, from how its dependencies ended.
type TriggerRule string

const (
	// TriggerRuleAllSuccess, the default, runs once every dependency
	// completed, and skips the task if any dependency failed or was skipped.
	TriggerRuleAllSuccess TriggerRule = "all_success"
	// TriggerRuleAllDone runs once every dependency has finished, however.
	TriggerRuleAllDone TriggerRule = "all_done"
	// TriggerRuleOneFailed runs once any dependency failed, and skips the task
	// if every dependency finished without failing.
	TriggerRuleOneFailed TriggerRule = "one_failed"
)

// ArtifactRef describes a file artifact stored in MinIO.
// Path is always relative to the task's artifact directory.
//
//	MinIO key: artifacts/{workflow_exec_id}/{task_def_id}/{Path}
type ArtifactRef struct {
	Path        string `json:"path"`                  // e.g. "output.json" or "results/data.csv"
	Description string `json:"description,omitempty"` // human-readable label shown in UI
}

// ContainerSpec holds security and resource settings for the isolated container.
type ContainerSpec struct {
	Image     string            `json:"image"`                // e.g. "python:3.12-slim"
	MemoryMB  int64             `json:"memory_mb,omitempty"`  // hard memory limit (default 256)
	CPUMillis int64             `json:"cpu_millis,omitempty"` // CPU quota in milli-CPUs (default 500)
	Env       map[string]string `json:"env,omitempty"`        // extra env vars injected into container
	WorkDir   string            `json:"work_dir,omitempty"`   // working directory inside container (default /workspace)
}

// RetryPolicy defines retry behavior for tasks
type RetryPolicy struct {
	MaxRetries      int           `json:"max_retries"`
	InitialDelay    time.Duration `json:"initial_delay"`
	MaxDelay        time.Duration `json:"max_delay"`
	BackoffMultiple float64       `json:"backoff_multiplier"`
	Jitter          bool          `json:"jitter"`
}

// TaskDefinition defines a single task within a workflow DAG
// ArtifactsIn  — paths produced by dependency tasks that this task needs.
//
//	The worker downloads these from MinIO and places them in the
//	container's /workspace before starting execution.
//
// ArtifactsOut — paths this task will produce inside /workspace.
//
//	The worker uploads these to MinIO after the container exits.
//
// Container    — Docker image and resource limits for the isolated executor.
//
//	Optional for code task types (data_transform, generic, ml_inference),
//	which always run in a container; nil applies the defaults. For the
//	other types, nil runs the task in-process.
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
	Container    *ContainerSpec    `json:"container,omitempty"`
	ArtifactsIn  []ArtifactRef     `json:"artifacts_in,omitempty"`
	ArtifactsOut []ArtifactRef     `json:"artifacts_out,omitempty"`
	TriggerRule  TriggerRule       `json:"trigger_rule,omitempty"`
	// When is a template that must render to "true" for the task to run, as
	// in {{ eq .payload.env "prod" }}; anything else skips it. It sees what
	// config templates see and is checked once TriggerRule is met.
	When string `json:"when,omitempty"`
}

// UsesTriggerRules reports whether any task sets a rule other than the
// default, or a When condition. Such a workflow lets failures and skips
// propagate through the rules instead of failing fast.
func (d *WorkflowDefinition) UsesTriggerRules() bool {
	for _, t := range d.Tasks {
		if (t.TriggerRule != "" && t.TriggerRule != TriggerRuleAllSuccess) || t.When != "" {
			return true
		}
	}
	return false
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
	// Schedule is a cron expression (five fields or a descriptor, UTC) that
	// starts a run each time it fires; empty means none.
	Schedule string `json:"schedule,omitempty"`
	// NextRunAt is when Schedule next fires. The server sets it; input is ignored.
	NextRunAt *time.Time      `json:"next_run_at,omitempty"`
	Alerts    *WorkflowAlerts `json:"alerts,omitempty"`
	Webhook   *WebhookTrigger `json:"webhook,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// WorkflowAlerts names where a finished run's outcome is posted. A cancelled
// run posts nothing: someone chose to stop it.
type WorkflowAlerts struct {
	OnSuccess *AlertTarget `json:"on_success,omitempty"`
	OnFailure *AlertTarget `json:"on_failure,omitempty"`
}

// WebhookTrigger lets POST /api/hooks/{id} start a run when the request is
// signed with the named secret (HMAC-SHA256 over "<timestamp>.<body>").
type WebhookTrigger struct {
	Secret string `json:"secret"`
}

// AlertTarget is an http or https URL that receives the alert as a JSON POST.
type AlertTarget struct {
	URL string `json:"url"`
}

// WorkflowRevision is one saved state of a workflow definition. Revisions
// count up from 1 per workflow and are never changed.
type WorkflowRevision struct {
	Revision  int       `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
}

// ResolvedArtifact is an ArtifactRef with its fully-qualified MinIO key resolved.
type ResolvedArtifact struct {
	Path     string `json:"path"`      // relative path inside container workspace
	MinioKey string `json:"minio_key"` // full MinIO object key
	Size     int64  `json:"size"`      // bytes, set after upload
}

// WorkflowExecution is a runtime instance of a WorkflowDefinition
type WorkflowExecution struct {
	ID             string            `json:"id"`
	WorkflowID     string            `json:"workflow_id"`
	WorkflowName   string            `json:"workflow_name"`
	Status         WorkflowStatus    `json:"status"`
	Tasks          []*TaskExecution  `json:"tasks" tstype:"TaskExecution[]"`
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
	ID               string             `json:"id"`
	WorkflowExecID   string             `json:"workflow_exec_id"`
	TaskDefinitionID string             `json:"task_definition_id"`
	TaskName         string             `json:"task_name"`
	TaskType         string             `json:"task_type"`
	Status           TaskStatus         `json:"status"`
	RetryCount       int                `json:"retry_count"`
	MaxRetries       int                `json:"max_retries"`
	WorkerID         string             `json:"worker_id,omitempty"`
	QueuedAt         *time.Time         `json:"queued_at,omitempty"`
	StartedAt        *time.Time         `json:"started_at,omitempty"`
	CompletedAt      *time.Time         `json:"completed_at,omitempty"`
	NextRetryAt      *time.Time         `json:"next_retry_at,omitempty"`
	Output           json.RawMessage    `json:"output,omitempty"`
	Error            string             `json:"error,omitempty"`
	Logs             []LogEntry         `json:"logs,omitempty"`
	Metadata         map[string]string  `json:"metadata,omitempty"`
	Duration         *time.Duration     `json:"duration,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	ArtifactsIn      []ResolvedArtifact `json:"artifacts_in,omitempty"`
	ArtifactsOut     []ResolvedArtifact `json:"artifacts_out,omitempty"`
}

// LogEntry represents a single log line from a task execution
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level" tstype:"'info' | 'warn' | 'error' | 'debug'"`
	// Attempt is the retry_count that produced the entry: the row's logs span every attempt.
	Attempt int            `json:"attempt"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// TaskMessage is what gets enqueued in Redis for workers
type TaskMessage struct {
	TaskExecID       string             `json:"task_exec_id"`
	WorkflowExecID   string             `json:"workflow_exec_id"`
	WorkflowID       string             `json:"workflow_id"`
	TaskDefinitionID string             `json:"task_def_id"`
	TaskName         string             `json:"task_name"`
	TaskType         string             `json:"task_type"`
	Config           map[string]any     `json:"config"`
	RetryCount       int                `json:"retry_count"`
	MaxRetries       int                `json:"max_retries"`
	Timeout          time.Duration      `json:"timeout"`
	EnqueuedAt       time.Time          `json:"enqueued_at"`
	Container        *ContainerSpec     `json:"container,omitempty"`
	ArtifactsIn      []ResolvedArtifact `json:"artifacts_in,omitempty"`
	ArtifactsOut     []ArtifactRef      `json:"artifacts_out,omitempty"`
	// TemplateData is what the worker renders Config's templates against: the
	// trigger payload and the dependencies' outputs. Set only when Config has
	// templates.
	TemplateData map[string]any `json:"template_data,omitempty"`
	// TraceParent is the W3C traceparent of the dispatch span, so the
	// worker's span joins the run's trace.
	TraceParent string `json:"traceparent,omitempty"`
}

// TaskResult is what workers publish back
type TaskResult struct {
	TaskExecID     string `json:"task_exec_id"`
	WorkflowExecID string `json:"workflow_exec_id"`
	WorkerID       string `json:"worker_id"`
	// RetryCount is the attempt the worker ran. It is a pointer because
	// results published before the field existed omit it; see Attempt.
	RetryCount   *int               `json:"retry_count,omitempty"`
	Success      bool               `json:"success"`
	Output       json.RawMessage    `json:"output,omitempty"`
	Error        string             `json:"error,omitempty"`
	Logs         []LogEntry         `json:"logs,omitempty"`
	StartedAt    time.Time          `json:"started_at"`
	CompletedAt  time.Time          `json:"completed_at"`
	ArtifactsOut []ResolvedArtifact `json:"artifacts_out,omitempty"`
	// TraceParent is the W3C traceparent of the worker's span, so processing
	// the result joins the run's trace.
	TraceParent string `json:"traceparent,omitempty"`
}

// Attempt returns the attempt r reports, or -1 when r predates the field.
// -1 makes Store.TransitionTask skip its attempt guard, so such a result is
// still checked against the task's status but never dropped for its attempt.
func (r *TaskResult) Attempt() int {
	if r.RetryCount == nil {
		return -1
	}
	return *r.RetryCount
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
	WSEventWorkflowCancelled = "workflow.cancelled"
	WSEventTaskQueued        = "task.queued"
	WSEventTaskStarted       = "task.started"
	WSEventTaskCompleted     = "task.completed"
	WSEventTaskFailed        = "task.failed"
	WSEventTaskRetrying      = "task.retrying"
	WSEventTaskSkipped       = "task.skipped"
	WSEventTaskLog           = "task.log"
	WSEventMetrics           = "metrics.update"
)
