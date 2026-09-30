import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, wsUrl } from './useApi'

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
