import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import App from './App.vue'
import { navRoutes } from './router'
import { api } from './composables/useApi'
import { principal } from './composables/useSession'

vi.mock('./composables/useApi', () => ({
	api: { get: vi.fn() },
	WS_URL: 'ws://test',
}))
const ws = vi.hoisted(() => ({ connect: vi.fn(), disconnect: vi.fn(), subscribe: vi.fn() }))
vi.mock('./stores/websocket', () => ({ useWebSocketStore: () => ws }))

const get = vi.mocked(api.get)

// The session ref is module state, so an App left mounted would react to the next test's sign-in.
enableAutoUnmount(afterEach)

async function loadAt(path: string) {
	const router = createRouter({ history: createMemoryHistory(), routes: navRoutes })
	router.push(path)
	await router.isReady()
	const pinia = createPinia()
	setActivePinia(pinia)
	const wrapper = mount(App, { global: { plugins: [pinia, router] } })
	await flushPromises()
	return wrapper
}

const calls = (prefix: string) => get.mock.calls.filter(([p]) => p.startsWith(prefix)).length

describe.each([
	['signed out', null],
	['signed in', { name: 'me', role: 'viewer' as const }],
])('initial fetches (%s)', (_, who) => {
	beforeEach(() => {
		principal.value = who
		get.mockReset()
		get.mockImplementation(async (path: string) =>
			path.startsWith('/api/workflows')
				? { workflows: [] }
				: path.startsWith('/api/executions')
					? { executions: [] }
					: {},
		)
	})

	it('requests workflows once when loading the builder', async () => {
		await loadAt('/builder')
		expect(calls('/api/workflows')).toBe(1)
	})

	it('requests executions once when loading the executions page', async () => {
		await loadAt('/executions')
		expect(calls('/api/executions')).toBe(1)
	})

	it('requests executions once when loading the logs page', async () => {
		await loadAt('/logs')
		expect(calls('/api/executions')).toBe(1)
	})
})

describe('session in the header', () => {
	beforeEach(() => {
		ws.connect.mockReset()
		ws.disconnect.mockReset()
		get.mockReset()
		get.mockResolvedValue({})
	})

	it('hides the shell and keeps the socket closed without a session', async () => {
		principal.value = null
		const wrapper = await loadAt('/builder')
		expect(wrapper.find('[data-testid="session-principal"]').exists()).toBe(false)
		expect(ws.connect).not.toHaveBeenCalled()
	})

	it('shows the principal and connects once signed in, and disconnects on sign-out', async () => {
		principal.value = { name: 'ops-bot', role: 'operator' }
		const wrapper = await loadAt('/builder')
		expect(wrapper.get('[data-testid="session-principal"]').text()).toContain('ops-bot')
		expect(wrapper.get('[data-testid="session-principal"]').text()).toContain('operator')
		expect(ws.connect).toHaveBeenCalledTimes(1)

		principal.value = null
		await flushPromises()
		expect(ws.disconnect).toHaveBeenCalled()
	})
})
