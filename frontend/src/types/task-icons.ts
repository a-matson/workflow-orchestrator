export const TASK_TYPE_ICONS: Record<string, string> = {
	http_request: '🌐',
	data_transform: '⚡',
	database_query: '🗄️',
	ml_inference: '🧠',
	notification: '📨',
	generic: '⚙️',
}

export const TASK_TYPE_LABELS: Record<string, string> = {
	http_request: 'HTTP Request',
	data_transform: 'Data Transform',
	database_query: 'Database Query',
	ml_inference: 'ML Inference',
	notification: 'Notification',
	generic: 'Generic',
}

export type TASK_TYPE = keyof typeof TASK_TYPE_ICONS

export function getTaskTypeIcon(type: string): string {
	return TASK_TYPE_ICONS[type] || '⚙️'
}

export function getTaskTypeLabel(type: string): string {
	return TASK_TYPE_LABELS[type] || type || 'Generic'
}
