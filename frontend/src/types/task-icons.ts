// types-icons.ts — extends the TASK_TYPES constant with icon field
// Import this alongside types/index.ts when rendering task type badges

export const TASK_TYPE_ICONS: Record<string, string> = {
  http_request:    '🌐',
  data_transform:  '⚡',
  database_query:  '🗄️',
  ml_inference:    '🧠',
  notification:    '📨',
  generic:         '⚙️',
}

export function getTaskTypeIcon(type: string): string {
  return TASK_TYPE_ICONS[type] ?? '⚙️'
}

export function getTaskTypeLabel(type: string): string {
  const labels: Record<string, string> = {
    http_request:   'HTTP Request',
    data_transform: 'Data Transform',
    database_query: 'Database Query',
    ml_inference:   'ML Inference',
    notification:   'Notification',
    generic:        'Generic',
  }
  return labels[type] ?? type
}
