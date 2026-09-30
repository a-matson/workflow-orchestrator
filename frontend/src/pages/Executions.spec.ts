import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type * as UseApi from '../composables/useApi'
import Executions from './Executions.vue'
import { api, ApiError } from '../composables/useApi'

vi.mock('../composables/useApi', async (orig) => ({
	...(await orig<typeof UseApi>()),
	api: { get: vi.fn() },
}))
vi.mock('../stores/websocket', () => ({ useWebSocketStore: () => ({ subscribe: vi.fn() }) }))
vi.mock('vue-router', () => ({
	useRoute: () => ({ params: { execId: 'e1' } }),
	useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))

const T0 = new Date('2026-01-01T12:00:00Z')

function runningExec() {
	const startedAt = new Date(T0.getTime() - 65_000).toISOString()
	return {
		id: 'e1',
		status: 'running',
		workflow_name: 'wf',
		started_at: startedAt,
		tasks: [{ id: 't1', task_name: 'a', status: 'running', retry_count: 0, started_at: startedAt }],
	}
}

// Unmounted in afterEach so the shared clock's subscriber count returns to zero between tests.
const mounted: { unmount: () => void }[] = []

async function mountRunning() {
	vi.mocked(api.get).mockImplementation(async (path: string) =>
		path.startsWith('/api/executions?') ? { executions: [runningExec()] } : runningExec(),
	)
	const w = mount(Executions)
	mounted.push(w)
	await flushPromises()
	return w
}

describe('Executions elapsed timers', () => {
	beforeEach(() => {
		setActivePinia(createPinia())
		vi.useFakeTimers()
		vi.setSystemTime(T0)
	})
	afterEach(() => {
		mounted.splice(0).forEach((w) => w.unmount())
		vi.useRealTimers()
	})

	it('ticks the run elapsed time without any new event, in minutes and seconds', async () => {
		const w = await mountRunning()
		expect(w.get('[data-testid="exec-elapsed"]').text()).toBe('1m 5s')

		await vi.advanceTimersByTimeAsync(2000)

		expect(w.get('[data-testid="exec-elapsed"]').text()).toBe('1m 7s')
	})

	it('formats a running task duration in minutes and seconds too', async () => {
		const w = await mountRunning()
		await vi.advanceTimersByTimeAsync(2000)

		expect(w.get('[data-testid="task-dur"]').text()).toBe('1m 7s')
	})
})

describe('Executions deep link', () => {
	beforeEach(() => setActivePinia(createPinia()))

	it('shows a not-found state when the linked run does not exist', async () => {
		vi.mocked(api.get).mockImplementation(async (path: string) => {
			if (path.startsWith('/api/executions?')) return { executions: [] }
			throw new ApiError(404, 'execution not found')
		})
		const w = mount(Executions)
		await flushPromises()

		expect(w.find('[data-testid="exec-not-found"]').exists()).toBe(true)
		w.unmount()
	})
})
