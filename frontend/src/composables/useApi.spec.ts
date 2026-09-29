import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, wsUrl } from './useApi'

afterEach(() => vi.unstubAllGlobals())

describe('wsUrl', () => {
	it.each([
		['http:', 'localhost:5173', 'ws://localhost:5173/ws'],
		['https:', 'fluxor.example.com', 'wss://fluxor.example.com/ws'],
	])('maps %s page to the matching ws scheme', (protocol, host, want) => {
		vi.stubGlobal('location', { protocol, host })
		expect(wsUrl()).toBe(want)
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
