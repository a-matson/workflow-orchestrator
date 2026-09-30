import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import Login from './Login.vue'
import { handleExpiredSession, requireSession } from '../router'
import { api, setUnauthorizedHandler } from '../composables/useApi'
import { principal } from '../composables/useSession'

const json = (status: number, body: unknown) =>
	Promise.resolve(
		new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }),
	)

let fetchMock: ReturnType<typeof vi.fn>

function makeRouter() {
	const router = createRouter({
		history: createMemoryHistory(),
		routes: [
			{ path: '/login', name: 'login', component: Login, meta: { public: true } },
			{ path: '/executions', name: 'executions', component: { template: '<div />' } },
		],
	})
	router.beforeEach(requireSession)
	return router
}

async function visitAndLogin(apiKey: string) {
	const router = makeRouter()
	await router.push('/executions')
	const wrapper = mount(Login, { global: { plugins: [router] } })
	await wrapper.get('[data-testid="login-key"]').setValue(apiKey)
	await wrapper.get('[data-testid="login-form"]').trigger('submit')
	await flushPromises()
	return { router, wrapper }
}

beforeEach(() => {
	principal.value = null
	fetchMock = vi.fn((url: string, init?: RequestInit) => {
		if (url.endsWith('/api/session') && init?.method === 'GET')
			return json(401, { error: 'authentication required' })
		return json(404, { error: 'unexpected ' + url })
	})
	vi.stubGlobal('fetch', fetchMock)
})

describe('router guard', () => {
	it('sends an unauthenticated visitor to /login, keeping the target', async () => {
		const router = makeRouter()
		await router.push('/executions')
		expect(router.currentRoute.value.name).toBe('login')
		expect(router.currentRoute.value.query.redirect).toBe('/executions')
	})
})

describe('login page', () => {
	it('posts the key and routes to the requested page', async () => {
		fetchMock.mockImplementation((url: string, init?: RequestInit) => {
			if (url.endsWith('/api/session') && init?.method === 'POST')
				return json(200, { name: 'me', role: 'admin' })
			return json(401, { error: 'authentication required' })
		})
		const { router } = await visitAndLogin('flx_secret')

		const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')
		expect(post?.[0]).toMatch(/\/api\/session$/)
		expect(JSON.parse(post?.[1]?.body as string)).toEqual({ api_key: 'flx_secret' })
		expect(principal.value).toEqual({ name: 'me', role: 'admin' })
		expect(router.currentRoute.value.fullPath).toBe('/executions')
	})

	it('shows the error on a rejected key and stays on /login', async () => {
		fetchMock.mockImplementation(() => json(401, { error: 'invalid API key' }))
		const { router, wrapper } = await visitAndLogin('flx_wrong')

		expect(wrapper.get('[data-testid="login-error"]').text()).toContain('invalid API key')
		expect(router.currentRoute.value.name).toBe('login')
		expect(principal.value).toBeNull()
	})
})

describe('expired session', () => {
	it('redirects to /login once when several calls get 401', async () => {
		principal.value = { name: 'me', role: 'viewer' }
		const router = makeRouter()
		await router.push('/executions')
		const push = vi.spyOn(router, 'push')
		setUnauthorizedHandler(handleExpiredSession(router))
		fetchMock.mockImplementation(() => json(401, { error: 'authentication required' }))

		await Promise.allSettled([api.get('/api/workflows'), api.get('/api/executions')])
		await flushPromises()

		expect(push).toHaveBeenCalledTimes(1)
		expect(router.currentRoute.value.name).toBe('login')
		expect(router.currentRoute.value.query.redirect).toBe('/executions')
		expect(principal.value).toBeNull()
	})
})
