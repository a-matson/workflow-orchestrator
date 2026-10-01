import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type * as UseApi from '../composables/useApi'
import Logs from './Logs.vue'
import { api } from '../composables/useApi'

vi.mock('../composables/useApi', async (orig) => ({
	...(await orig<typeof UseApi>()),
	api: { get: vi.fn() },
}))
vi.mock('../stores/websocket', () => ({ useWebSocketStore: () => ({ subscribe: vi.fn() }) }))
vi.mock('vue-router', () => ({ useRoute: () => ({ params: { execId: 'e1' } }) }))

const T0 = Date.parse('2026-01-01T12:00:00Z')
const line = (i: number, task: string) => ({
	timestamp: new Date(T0 + i * 1000).toISOString(),
	level: 'info',
	message: `${task} line ${i}`,
})

// Two tasks whose lines interleave in time, 5,000 lines in all.
function bigExec() {
	const even = Array.from({ length: 2500 }, (_, k) => line(2 * k, 'a'))
	const odd = Array.from({ length: 2500 }, (_, k) => line(2 * k + 1, 'b'))
	return {
		id: 'e1',
		status: 'completed',
		workflow_name: 'wf',
		tasks: [
			{ id: 'ta', task_name: 'a', status: 'completed', logs: even },
			{ id: 'tb', task_name: 'b', status: 'completed', logs: odd },
		],
	}
}

describe('Logs with many lines (UI-8)', () => {
	beforeEach(() => setActivePinia(createPinia()))
	afterEach(() => vi.restoreAllMocks())

	it('renders a bounded window of the newest lines, in time order, and more on request', async () => {
		vi.mocked(api.get).mockImplementation(async (path: string) =>
			path.startsWith('/api/executions?') ? { executions: [bigExec()] } : bigExec(),
		)
		const w = mount(Logs)
		await flushPromises()

		const lines = () => w.findAll('.log-line')
		expect(lines().length).toBeLessThanOrEqual(500)
		expect(lines().at(-1)!.text()).toContain('b line 4999')
		expect(lines().at(-2)!.text()).toContain('a line 4998')

		await w.get('[data-testid="logs-show-earlier"]').trigger('click')
		expect(lines().length).toBeGreaterThan(500)
	})
})
