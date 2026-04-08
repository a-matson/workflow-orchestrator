// Core domain types matching the Go backend models

export type TaskStatus =
	| 'pending'
	| 'queued'
	| 'running'
	| 'completed'
	| 'failed'
	| 'retrying'
	| 'skipped'
	| 'dead_letter'
export type WorkflowStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled' | 'paused'

export interface RetryPolicy {
	max_retries: number
	initial_delay: number // nanoseconds
	max_delay: number
	backoff_multiplier: number
	jitter: boolean
}

export interface TaskDefinition {
	id: string
	name: string
	type: string
	dependencies: string[]
	config: Record<string, unknown>
	retry_policy?: RetryPolicy
	timeout?: number
	max_parallel?: number
	metadata?: Record<string, string>
}

export interface WorkflowDefinition {
	id: string
	name: string
	description: string
	version: string
	tasks: TaskDefinition[]
	global_retry?: RetryPolicy
	max_parallel: number
	tags?: Record<string, string>
	created_at: string
	updated_at: string
}

export interface LogEntry {
	timestamp: string
	level: 'info' | 'warn' | 'error' | 'debug'
	message: string
	fields?: Record<string, unknown>
}

export interface TaskExecution {
	id: string
	workflow_exec_id: string
	task_definition_id: string
	task_name: string
	task_type: string
	status: TaskStatus
	retry_count: number
	max_retries: number
	worker_id?: string
	queued_at?: string
	started_at?: string
	completed_at?: string
	next_retry_at?: string
	output?: unknown
	error?: string
	logs?: LogEntry[]
	metadata?: Record<string, string>
	duration?: number
	created_at: string
	updated_at: string
}

export interface WorkflowExecution {
	id: string
	workflow_id: string
	workflow_name: string
	status: WorkflowStatus
	tasks: TaskExecution[]
	started_at?: string
	completed_at?: string
	trigger_payload?: Record<string, unknown>
	error?: string
	metadata?: Record<string, string>
	created_at: string
	updated_at: string
}

export interface WebSocketEvent {
	type: string
	payload: unknown
}

export interface PlatformMetrics {
	workflows_started: number
	workflows_completed: number
	workflows_failed: number
	tasks_dispatched: number
	tasks_completed: number
	tasks_failed: number
	tasks_retried: number
	tasks_dead_lettered: number
	active_workflows: number
	queue_depth: number
	retry_queue_depth: number
	ws_clients: number
}

// DAG editor types (Vue Flow)
export interface DAGNode {
	id: string
	type: 'taskNode'
	position: { x: number; y: number }
	data: {
		taskDef: TaskDefinition
		status?: TaskStatus
		taskExec?: TaskExecution
	}
}

export interface DAGEdge {
	id: string
	source: string
	target: string
	type: 'smoothstep'
	animated?: boolean
	style?: Record<string, string>
}

// Status color mapping
export const STATUS_COLORS: Record<TaskStatus | WorkflowStatus, string> = {
	pending: '#6B7280',
	queued: '#8B5CF6',
	running: '#3B82F6',
	completed: '#10B981',
	failed: '#EF4444',
	retrying: '#F59E0B',
	skipped: '#9CA3AF',
	dead_letter: '#991B1B',
	cancelled: '#DC2626',
	paused: '#F59E0B',
}

export const STATUS_BG: Record<TaskStatus | WorkflowStatus, string> = {
	pending: '#F3F4F6',
	queued: '#EDE9FE',
	running: '#DBEAFE',
	completed: '#D1FAE5',
	failed: '#FEE2E2',
	retrying: '#FEF3C7',
	skipped: '#E5E7EB',
	dead_letter: '#FEE2E2',
	cancelled: '#FEE2E2',
	paused: '#FEF3C7',
}

export const TASK_TYPES = [
	{ value: 'http_request', label: 'HTTP Request' },
	{ value: 'data_transform', label: 'Data Transform' },
	{ value: 'database_query', label: 'Database Query' },
	{ value: 'ml_inference', label: 'ML Inference' },
	{ value: 'notification', label: 'Notification' },
	{ value: 'generic', label: 'Generic Task' },
]
