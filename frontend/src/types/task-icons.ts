// types-icons.ts — extends the TASK_TYPES constant with icon field
// Import this alongside types/index.ts when rendering task type badges

export type TASK_TYPE = keyof typeof TASK_TYPE_ICONS

export function transformTaskToType<T>([a, b, c, d, e, f]: T[]) {
	return {
		http_request: a,
		data_transform: b,
		database_query: c,
		ml_inference: d,
		notification: e,
		generic: f,
	}
}

export const TASK_TYPE_ICONS = transformTaskToType(['🌐', '⚡', '🗄️', '🧠', '📨', '⚙️'])

export function getTaskTypeIcon(type: TASK_TYPE) {
	return TASK_TYPE_ICONS[type]
}

export function getTaskTypeLabel(type: TASK_TYPE): string {
	return transformTaskToType([
		'HTTP Request',
		'Data Transform',
		'Database Query',
		'ML Inference',
		'Notification',
		'Generic',
	])[type]
}
