import { describe, expect, it, vi } from 'vitest'
import { createErrorReporter, type ClientErrorReport } from './useErrorReporting'

describe('createErrorReporter', () => {
	it('sends an unhandled error once, however often it recurs', () => {
		const send = vi.fn(async (_: ClientErrorReport) => undefined)
		const report = createErrorReporter(send)

		const err = new Error('boom')
		report(err, 'vue')
		report(err, 'vue')
		report(new Error('boom'), 'vue')

		expect(send).toHaveBeenCalledTimes(1)
		expect(send.mock.calls[0][0]).toMatchObject({ message: 'boom', source: 'vue' })
		expect(send.mock.calls[0][0].stack).toContain('boom')
	})

	it('stops sending past the per-minute limit, and resumes a minute later', () => {
		let now = 0
		const send = vi.fn(async (_: ClientErrorReport) => undefined)
		const report = createErrorReporter(send, { maxPerMinute: 2, now: () => now })

		for (let i = 0; i < 5; i++) report(new Error(`e${i}`), 'window')
		expect(send).toHaveBeenCalledTimes(2)

		now = 60_001
		report(new Error('later'), 'window')
		expect(send).toHaveBeenCalledTimes(3)
	})

	it('never throws, even when sending fails', () => {
		const report = createErrorReporter(() => Promise.reject(new Error('offline')))
		expect(() => report('not an Error', 'promise')).not.toThrow()
	})
})
