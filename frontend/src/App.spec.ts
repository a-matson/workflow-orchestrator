import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import App from './App.vue'
import { navRoutes } from './router'
import { api } from './composables/useApi'

vi.mock('./composables/useApi', () => ({
	api: { get: vi.fn() },
	WS_URL: 'ws://test',
}))
vi.mock('./stores/websocket', () => ({
	useWebSocketStore: () => ({ connect: vi.fn(), disconnect: vi.fn(), subscribe: vi.fn() }),
}))

const get = vi.mocked(api.get)

async function loadAt(path: string) {
	const router = createRouter({ history: createMemoryHistory(), routes: navRoutes })
	router.push(path)
	await router.isReady()
	const pinia = createPinia()
	setActivePinia(pinia)
	mount(App, { global: { plugins: [pinia, router] } })
	await flushPromises()
}

const calls = (prefix: string) => get.mock.calls.filter(([p]) => p.startsWith(prefix)).length

describe('initial fetches', () => {
	beforeEach(() => {
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
