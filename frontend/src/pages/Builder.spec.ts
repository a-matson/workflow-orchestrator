import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Builder from './Builder.vue'
import { api } from '../composables/useApi'
import type { WorkflowDefinition } from '../types'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('../composables/useApi', () => ({
	api: { get: vi.fn(), put: vi.fn(), post: vi.fn() },
	WS_URL: 'ws://test/ws',
	wsUrl: () => 'ws://test/ws',
}))

const existing = {
	id: 'wf1',
	name: 'nightly',
	description: 'nightly ETL',
	version: '2.3.1',
	max_parallel: 4,
	tags: { team: 'data' },
	global_retry: {
		max_retries: 5,
		initial_delay: 1e9,
		max_delay: 60e9,
		backoff_multiplier: 3,
		jitter: false,
	},
	tasks: [
		{
			id: 't1',
			name: 'extract',
			type: 'generic',
			dependencies: [],
			config: {},
			retry_policy: { max_retries: 1, initial_delay: 1e9, max_delay: 2e9, backoff_multiplier: 2 },
			timeout: 60e9,
		},
	],
	created_at: '',
	updated_at: '',
} as unknown as WorkflowDefinition

describe('Builder save', () => {
	beforeEach(() => {
		setActivePinia(createPinia())
		vi.mocked(api.get).mockImplementation(async (path: string) =>
			path.startsWith('/api/workflows?') ? { workflows: [existing] } : existing,
		)
		vi.mocked(api.put).mockImplementation(async (_p: string, body: unknown) => body)
	})

	it('keeps the loaded definition fields it does not edit', async () => {
		const wrapper = mount(Builder, { global: { provide: { showToast: vi.fn() } } })
		await flushPromises()
		await wrapper.get('[data-testid="wf-item"]').trigger('click')
		await flushPromises()

		await wrapper.get('[data-testid="save-workflow"]').trigger('click')
		await flushPromises()

		expect(api.put).toHaveBeenCalledTimes(1)
		expect(vi.mocked(api.put).mock.calls[0][1]).toMatchObject({
			id: 'wf1',
			description: existing.description,
			version: existing.version,
			max_parallel: existing.max_parallel,
			tags: existing.tags,
			global_retry: existing.global_retry,
		})
	})

	it('durations save as integer nanoseconds', async () => {
		const wrapper = mount(Builder, {
			attachTo: document.body,
			global: { provide: { showToast: vi.fn() } },
		})
		await flushPromises()
		await wrapper.get('[data-testid="wf-item"]').trigger('click')
		await flushPromises()
		await wrapper.get('.vue-flow__node').trigger('click')
		await flushPromises()

		// 1.001 * 1e9 === 1000999999.9999999 in IEEE doubles (1.1 happens to be exact).
		for (const id of ['task-timeout', 'retry-initial-delay']) {
			const input = wrapper.get(`[data-testid="${id}"]`)
			await input.setValue('1.001')
			await input.trigger('change')
		}
		await wrapper.get('[data-testid="save-workflow"]').trigger('click')
		await flushPromises()

		const saved = vi.mocked(api.put).mock.calls[0][1] as WorkflowDefinition
		const task = saved.tasks[0]
		expect(task.timeout).toBe(1001000000)
		expect(task.retry_policy?.initial_delay).toBe(1001000000)
		expect(Number.isInteger(task.timeout)).toBe(true)
		expect(Number.isInteger(task.retry_policy?.initial_delay)).toBe(true)
		wrapper.unmount()
	})
})
