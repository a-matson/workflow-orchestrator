import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import TaskConfigFields from './TaskConfigFields.vue'
import Builder from '../../pages/Builder.vue'
import { api } from '../../composables/useApi'
import { WORKFLOW_TEMPLATES } from '../../composables/useTemplates'
import type { WorkflowDefinition } from '../../types'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('../../composables/useApi', () => ({
	api: { get: vi.fn(), put: vi.fn(), post: vi.fn() },
	WS_URL: 'ws://test/ws',
	wsUrl: () => 'ws://test/ws',
}))

// The config keys the executors read (backend/internal/worker): execHTTP,
// execDBQuery and execNotification in worker.go; buildCommand, buildEnv and
// mlInferenceCommand in container_executor.go. A key outside this table is
// silently ignored at run time.
const EXECUTOR_KEYS: Record<string, string[]> = {
	http_request: ['method', 'url', 'headers', 'body', 'timeout_ms'],
	database_query: ['connection_string', 'query', 'max_rows'],
	notification: ['notify_type', 'channel', 'message'],
	generic: ['command', 'args', 'env', 'script'],
	data_transform: ['script', 'env'],
	ml_inference: ['model_name', 'input_path', 'output_path', 'batch_size', 'env'],
}

describe('TaskConfigFields', () => {
	it.each(Object.entries(EXECUTOR_KEYS))(
		'%s renders exactly the keys its executor reads',
		(type, keys) => {
			const wrapper = mount(TaskConfigFields, { props: { type, config: {} } })
			const rendered = wrapper
				.findAll('[data-testid^="cfg-"]')
				.map((el) => el.attributes('data-testid')!.slice('cfg-'.length))
			expect(rendered.sort()).toEqual([...keys].sort())
		},
	)
})

describe('workflow templates', () => {
	it('only use config keys the executors read', () => {
		const unread: string[] = []
		for (const tpl of WORKFLOW_TEMPLATES) {
			for (const task of tpl.tasks) {
				const allowed = EXECUTOR_KEYS[task.type] ?? []
				for (const key of Object.keys(task.config ?? {})) {
					if (!allowed.includes(key)) unread.push(`${tpl.name}/${task.id} (${task.type}): ${key}`)
				}
			}
		}
		expect(unread).toEqual([])
	})
})

describe('Builder container section', () => {
	it('offers image and resources for code tasks without an isolation toggle', async () => {
		setActivePinia(createPinia())
		const wf = {
			id: 'wf1',
			name: 'w',
			version: '1',
			tasks: [{ id: 't1', name: 'run', type: 'generic', dependencies: [], config: {} }],
			created_at: '',
			updated_at: '',
		} as unknown as WorkflowDefinition
		vi.mocked(api.get).mockImplementation(async (path: string) =>
			path.startsWith('/api/workflows?') ? { workflows: [wf] } : wf,
		)
		const wrapper = mount(Builder, {
			attachTo: document.body,
			global: { provide: { showToast: vi.fn() } },
		})
		await flushPromises()
		await wrapper.get('[data-testid="wf-item"]').trigger('click')
		await flushPromises()
		await wrapper.get('.vue-flow__node').trigger('click')
		await flushPromises()

		expect(wrapper.text()).not.toContain('Container Isolation')
		expect(wrapper.find('[data-testid="container-image"]').exists()).toBe(true)
		expect(wrapper.find('[data-testid="container-memory"]').exists()).toBe(true)
		expect(wrapper.find('[data-testid="container-cpu"]').exists()).toBe(true)
		wrapper.unmount()
	})
})
