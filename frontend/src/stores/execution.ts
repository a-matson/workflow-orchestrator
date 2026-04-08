import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api } from '../composables/useApi'
import type { WorkflowExecution, TaskExecution, LogEntry } from '../types'

export const useExecutionStore = defineStore('executions', () => {
	const executions = ref<WorkflowExecution[]>([])
	const selectedId = ref<string | null>(null)
	const loading = ref(false)
	const taskLogs = ref<Record<string, LogEntry[]>>({}) // taskExecId → logs

	// ── Getters ────────────────────────────────────────────
	const selected = computed(() => executions.value.find((e) => e.id === selectedId.value) ?? null)

	const byStatus = computed(() => {
		const groups: Record<string, WorkflowExecution[]> = {}
		for (const e of executions.value) {
			;(groups[e.status] ??= []).push(e)
		}
		return groups
	})

	const activeCount = computed(
		() => executions.value.filter((e) => e.status === 'running' || e.status === 'pending').length,
	)

	// ── Actions ────────────────────────────────────────────
	async function fetchAll(limit = 50, offset = 0) {
		loading.value = true
		try {
			const res = await api.get<{ executions: WorkflowExecution[] }>(
				`/api/executions?limit=${limit}&offset=${offset}`,
			)
			executions.value = res.executions ?? []
		} finally {
			loading.value = false
		}
	}

	async function fetchOne(id: string) {
		const exec = await api.get<WorkflowExecution>(`/api/executions/${id}`)
		upsert(exec)
		return exec
	}

	async function fetchTaskLogs(taskExecId: string) {
		const res = await api.get<{ logs: LogEntry[] }>(`/api/tasks/${taskExecId}/logs`)
		taskLogs.value[taskExecId] = res.logs ?? []
		return res.logs
	}

	// ── WS event handlers ──────────────────────────────────
	function handleWorkflowEvent(payload: unknown) {
		const exec = payload as WorkflowExecution
		upsert(exec)
	}

	function handleTaskEvent(payload: unknown) {
		const task = payload as TaskExecution
		const exec = executions.value.find((e) => e.id === task.workflow_exec_id)
		if (!exec) return
		const idx = exec.tasks?.findIndex((t) => t.id === task.id) ?? -1
		if (idx >= 0) {
			exec.tasks[idx] = { ...exec.tasks[idx], ...task }
		} else {
			exec.tasks ??= []
			exec.tasks.push(task)
		}
	}

	function handleLogEvent(payload: unknown) {
		const ev = payload as { task_exec_id: string; entry: LogEntry }
		;(taskLogs.value[ev.task_exec_id] ??= []).push(ev.entry)
		// Also append to the in-memory task
		for (const exec of executions.value) {
			const task = exec.tasks?.find((t) => t.id === ev.task_exec_id)
			if (task) {
				task.logs ??= []
				task.logs.push(ev.entry)
				break
			}
		}
	}

	// ── Internal helpers ────────────────────────────────────
	function upsert(exec: WorkflowExecution) {
		const idx = executions.value.findIndex((e) => e.id === exec.id)
		if (idx >= 0) {
			executions.value[idx] = { ...executions.value[idx], ...exec }
		} else {
			executions.value.unshift(exec)
		}
	}

	function select(id: string | null) {
		selectedId.value = id
	}

	return {
		executions,
		selectedId,
		selected,
		byStatus,
		activeCount,
		loading,
		taskLogs,
		fetchAll,
		fetchOne,
		fetchTaskLogs,
		handleWorkflowEvent,
		handleTaskEvent,
		handleLogEvent,
		select,
		upsert,
	}
})
