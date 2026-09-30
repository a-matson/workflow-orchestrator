import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, setUnauthorizedHandler, wsUrl } from './useApi'

afterEach(() => {
	vi.unstubAllGlobals()
	vi.unstubAllEnvs()
	vi.resetModules()
})

describe('wsUrl', () => {
	it.each([
		['http:', 'localhost:5173', 'ws://localhost:5173/ws'],
		['https:', 'fluxor.example.com', 'wss://fluxor.example.com/ws'],
	])('maps %s page to the matching ws scheme', (protocol, host, want) => {
		vi.stubGlobal('location', { protocol, host })
		expect(wsUrl()).toBe(want)
	})
})

describe('wsUrl with VITE_API_URL', () => {
	// BASE_URL is read at module load, so each case re-imports the module.
	it.each([
		['https://api.example.com', 'wss://api.example.com/ws'],
		['http://api.example.com', 'ws://api.example.com/ws'],
	])('maps %s to the matching ws scheme', async (base, want) => {
		vi.stubEnv('VITE_API_URL', base)
		const mod = await import('./useApi')
		expect(mod.wsUrl('/ws')).toBe(want)
	})
})

describe('request helper', () => {
	it('sends Content-Type: application/json', async () => {
		const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) })
		vi.stubGlobal('fetch', fetchMock)
		await api.post('/api/x', { a: 1 })
		const [, init] = fetchMock.mock.calls[0]
		expect(init.headers).toEqual({ 'Content-Type': 'application/json' })
		expect(init.body).toBe('{"a":1}')
	})
})

describe('401 handling', () => {
	it('leaves /api/session 401s to the guard and the login page', async () => {
		const expired = vi.fn()
		setUnauthorizedHandler(expired)
		vi.stubGlobal(
			'fetch',
			vi.fn().mockResolvedValue({ ok: false, status: 401, json: async () => ({}) }),
		)
		await expect(api.post('/api/session', { api_key: 'x' })).rejects.toThrow()
		expect(expired).not.toHaveBeenCalled()
		await expect(api.get('/api/workflows')).rejects.toThrow()
		expect(expired).toHaveBeenCalledTimes(1)
	})

	it('resolves an empty 204 instead of failing to parse it', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 204 }))
		await expect(api.delete('/api/session')).resolves.toBeUndefined()
	})
})
