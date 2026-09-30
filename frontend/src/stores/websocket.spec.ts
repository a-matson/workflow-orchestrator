import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWebSocketStore } from './websocket'
import { useWorkflowStore } from './workflow'
import { api } from '../composables/useApi'

vi.mock('../composables/useApi', () => ({ api: { get: vi.fn() }, WS_URL: 'ws://test' }))

class FakeWebSocket {
	static OPEN = 1
	static instances: FakeWebSocket[] = []
	readyState = 0
	sent: string[] = []
	onopen: (() => void) | null = null
	onclose: ((e: { code: number; reason: string }) => void) | null = null
	onerror: ((e: unknown) => void) | null = null
	onmessage: ((e: { data: string }) => void) | null = null
	constructor(public url: string) {
		FakeWebSocket.instances.push(this)
	}
	send(data: string) {
		this.sent.push(data)
	}
	close(code = 1000, reason = '') {
		this.readyState = 3
		this.onclose?.({ code, reason })
	}
	open() {
		this.readyState = FakeWebSocket.OPEN
		this.onopen?.()
	}
	drop() {
		this.readyState = 3
		this.onclose?.({ code: 1006, reason: '' })
	}
}

const sockets = () => FakeWebSocket.instances
const get = vi.mocked(api.get)

describe('websocket store reconnect lifecycle', () => {
	beforeEach(() => {
		setActivePinia(createPinia())
		FakeWebSocket.instances = []
		vi.stubGlobal('WebSocket', FakeWebSocket)
		vi.useFakeTimers()
		vi.spyOn(Math, 'random').mockReturnValue(0.999)
		vi.spyOn(console, 'log').mockImplementation(() => {})
		vi.spyOn(console, 'error').mockImplementation(() => {})
		get.mockReset()
		get.mockResolvedValue({ executions: [] })
	})
	afterEach(() => {
		vi.useRealTimers()
		vi.unstubAllGlobals()
		vi.restoreAllMocks()
	})

	it('does not reconnect after an explicit disconnect', () => {
		const ws = useWebSocketStore()
		ws.connect()
		sockets()[0].open()

		ws.disconnect()
		vi.advanceTimersByTime(120_000)

		expect(sockets()).toHaveLength(1)
	})

	it('keeps retrying past 15 consecutive failures with a capped delay', () => {
		const ws = useWebSocketStore()
		ws.connect()
		for (let i = 0; i < 15; i++) {
			sockets().at(-1)!.drop()
			vi.advanceTimersByTime(30_000)
		}

		expect(sockets()).toHaveLength(16)
	})

	it('refetches executions and resubscribes after a dropped connection reopens', async () => {
		const ws = useWebSocketStore()
		const wf = useWorkflowStore()
		ws.connect()
		sockets()[0].open()
		ws.subscribe('e1')
		wf.selectedExecution = { id: 'e1' } as never
		get.mockClear()

		sockets()[0].drop()
		vi.advanceTimersByTime(30_000)
		sockets()[1].open()
		await vi.advanceTimersByTimeAsync(0)

		const paths = get.mock.calls.map(([p]) => p)
		expect(paths.some((p) => p.startsWith('/api/executions?'))).toBe(true)
		expect(paths).toContain('/api/executions/e1')
		expect(sockets()[1].sent).toContain(JSON.stringify({ type: 'subscribe', payload: 'e1' }))
	})
})
