import { ref, computed, watch, onUnmounted } from 'vue'
import { api } from './useApi'
import { useWebSocketStore } from '../stores/websocket'
import type { LogEntry, WorkflowExecution } from '../types'

export interface UseTaskLogsOptions {
	taskExecId?: string
	autoRefresh?: boolean
}

/**
 * Reactive task-scoped log state with WS-driven live updates.
 * Combines static logs from the API with streaming entries from WS events.
 */
export function useTaskLogs(
	execution: () => WorkflowExecution | null,
	// options: UseTaskLogsOptions = {},
) {
	const staticLogs = ref<Array<LogEntry & { _taskId: string; _taskName: string }>>([])
	const loading = ref(false)
	const error = ref<string | null>(null)
	const autoScroll = ref(true)

	const wsStore = useWebSocketStore()

	// Build enriched log list from all tasks in the execution
	const allLogs = computed(() => {
		const exec = execution()
		if (!exec) return staticLogs.value

		// Pre-allocate array and use a Map for O(1) deduplication (avoids heavy string concatenation)
		const logMap = new Map<string, LogEntry & { _taskId: string; _taskName: string }>()

		// 1. Process static logs
		for (const log of staticLogs.value) {
			logMap.set(`${log.timestamp}-${log._taskId}`, log)
		}

		// 2. Process active execution logs
		for (const task of exec.tasks ?? []) {
			if (!task.logs) continue
			for (const log of task.logs) {
				logMap.set(`${log.timestamp}-${task.id}`, {
					...log,
					_taskId: task.id,
					_taskName: task.task_name,
				})
			}
		}

		// Convert map values to array and sort efficiently
		return Array.from(logMap.values()).sort(
			(a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime(),
		)
	})

	// const filteredLogs = ref<typeof allLogs.value>([])

	// Level filter
	const levelFilter = ref<'all' | 'info' | 'warn' | 'error' | 'debug'>('all')
	const searchQuery = ref('')
	const taskFilter = ref<string>('')

	const displayLogs = computed(() => {
		return allLogs.value.filter((l) => {
			if (levelFilter.value !== 'all' && l.level !== levelFilter.value) return false
			if (taskFilter.value && l._taskId !== taskFilter.value) return false
			if (searchQuery.value) {
				const q = searchQuery.value.toLowerCase()
				return (
					l.message?.toLowerCase().includes(q) ||
					l._taskName?.toLowerCase().includes(q) ||
					JSON.stringify(l.fields ?? {})
						.toLowerCase()
						.includes(q)
				)
			}
			return true
		})
	})

	// Fetch logs from API for a specific task
	async function fetchTaskLogs(taskExecId: string) {
		loading.value = true
		error.value = null
		try {
			const res = await api.get<{ logs: LogEntry[]; task_id: string }>(
				`/api/tasks/${taskExecId}/logs`,
			)
			const exec = execution()
			const task = exec?.tasks.find((t) => t.id === taskExecId)
			const enriched = (res.logs ?? []).map((l) => ({
				...l,
				_taskId: taskExecId,
				_taskName: task?.task_name ?? taskExecId,
			}))
			// Merge into static logs
			staticLogs.value = [...staticLogs.value.filter((l) => l._taskId !== taskExecId), ...enriched]
		} catch (e) {
			error.value = `Failed to fetch logs: ${e}`
		} finally {
			loading.value = false
		}
	}

	// Watch for new log entries arriving via WS
	const unwatch = watch(
		() => wsStore.eventLog,
		(log) => {
			const latest = log[0]
			if (latest?.type === 'task.log') {
				// Re-read from execution store (WS store already merged it in)
				// displayLogs will automatically recompute from execution()
			}
		},
		{ deep: false },
	)

	onUnmounted(unwatch)

	function setLevelFilter(level: typeof levelFilter.value) {
		levelFilter.value = level
	}
	function setTaskFilter(id: string) {
		taskFilter.value = id
	}
	function setSearchQuery(q: string) {
		searchQuery.value = q
	}

	function exportLogs(): string {
		return displayLogs.value
			.map((l) => {
				const ts = new Date(l.timestamp).toISOString()
				const fields = Object.keys(l.fields ?? {}).length ? ' ' + JSON.stringify(l.fields) : ''
				return `[${ts}] ${l.level?.toUpperCase().padEnd(5)} [${l._taskName}] ${l.message}${fields}`
			})
			.join('\n')
	}

	function copyToClipboard() {
		navigator.clipboard.writeText(exportLogs())
	}

	return {
		allLogs,
		displayLogs,
		loading,
		error,
		autoScroll,
		levelFilter,
		searchQuery,
		taskFilter,
		fetchTaskLogs,
		setLevelFilter,
		setTaskFilter,
		setSearchQuery,
		exportLogs,
		copyToClipboard,
	}
}
