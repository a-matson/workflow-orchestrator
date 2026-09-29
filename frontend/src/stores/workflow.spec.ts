import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkflowStore } from './workflow'
import type { WorkflowExecution } from '../types'

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
})
