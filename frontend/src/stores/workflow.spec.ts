import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkflowStore } from './workflow'
import type { WorkflowExecution } from '../types'
import { api } from '../composables/useApi'

vi.mock('../composables/useApi', () => ({ api: { get: vi.fn() } }))

const execWithTask = () =>
	({
		id: 'e1',
		status: 'running',
		tasks: [{ id: 't1', logs: [] }],
	}) as unknown as WorkflowExecution

const logEvent = {
	workflow_exec_id: 'e1',
	task_exec_id: 't1',
	task_name: 'a',
	entry: { line: 'x' },
}

describe('workflow store', () => {
	beforeEach(() => setActivePinia(createPinia()))

	it('adds an unknown execution on workflow.started, then merges updates', () => {
		const store = useWorkflowStore()
		const exec = { id: 'e1', status: 'running', tasks: [] } as unknown as WorkflowExecution

		store.updateFromWsEvent('workflow.started', exec)
		expect(store.executions.map((e) => e.id)).toEqual(['e1'])

		store.updateFromWsEvent('workflow.completed', { id: 'e1', status: 'completed' })
		expect(store.executions).toHaveLength(1)
		expect(store.executions[0].status).toBe('completed')
	})

	describe('task.log', () => {
		const get = vi.mocked(api.get)
		beforeEach(() => get.mockReset())

		it('appends once when the selected execution is also in the list', async () => {
			get.mockResolvedValueOnce({ executions: [execWithTask()] })
			get.mockResolvedValueOnce(execWithTask())
			const store = useWorkflowStore()
			await store.fetchExecutions()
			await store.fetchExecution('e1')

			store.updateFromWsEvent('task.log', logEvent)

			expect(store.selectedExecution?.tasks[0].logs).toHaveLength(1)
			expect(store.executions[0].tasks[0].logs).toHaveLength(1)
		})

		it('appends once after a workflow.* event re-spreads the execution', async () => {
			get.mockResolvedValueOnce({ executions: [execWithTask()] })
			get.mockResolvedValueOnce(execWithTask())
			const store = useWorkflowStore()
			await store.fetchExecutions()
			await store.fetchExecution('e1')
			store.updateFromWsEvent('workflow.updated', { id: 'e1', status: 'running' })

			store.updateFromWsEvent('task.log', logEvent)

			expect(store.selectedExecution?.tasks[0].logs).toHaveLength(1)
		})

		it('still appends to the selected execution when it is not in the list', async () => {
			get.mockResolvedValueOnce(execWithTask())
			const store = useWorkflowStore()
			await store.fetchExecution('e1')
			store.executions = []

			store.updateFromWsEvent('task.log', logEvent)

			expect(store.selectedExecution?.tasks[0].logs).toHaveLength(1)
		})

		it('still appends to a listed execution that is not selected', async () => {
			get.mockResolvedValueOnce({ executions: [execWithTask()] })
			const store = useWorkflowStore()
			await store.fetchExecutions()

			store.updateFromWsEvent('task.log', logEvent)

			expect(store.executions[0].tasks[0].logs).toHaveLength(1)
		})
	})
})
