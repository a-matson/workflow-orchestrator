// API types are generated from backend/internal/models (see backend/tygo.yaml);
// this file adds the UI's own types on top.
export type {
	ArtifactRef,
	ContainerSpec,
	LogEntry,
	TaskDefinition,
	TaskExecution,
	TaskStatus,
	WebSocketEvent,
	WorkflowDefinition,
	WorkflowExecution,
	WorkflowStatus,
} from './generated'
import type { TaskDefinition, TaskExecution, TaskStatus, WorkflowStatus } from './generated'

export interface PlatformMetrics {
	workflows_started: number
	workflows_completed: number
	workflows_failed: number
	workflows_cancelled: number
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

// Mirrors runsUserCode in backend/internal/worker/worker.go: unknown types run as code too.
export const runsUserCode = (type: string) =>
	!['http_request', 'database_query', 'notification'].includes(type)

export const TASK_TYPES = [
	{ value: 'http_request', label: 'HTTP Request' },
	{ value: 'data_transform', label: 'Data Transform' },
	{ value: 'database_query', label: 'Database Query' },
	{ value: 'ml_inference', label: 'ML Inference' },
	{ value: 'notification', label: 'Notification' },
	{ value: 'generic', label: 'Generic Task' },
]
