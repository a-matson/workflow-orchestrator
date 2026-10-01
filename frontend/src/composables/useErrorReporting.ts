import type { App } from 'vue'

export interface ClientErrorReport {
	message: string
	stack: string
	source: string
	path: string
}

const MAX_MESSAGE = 500
const MAX_STACK = 4000

// createErrorReporter returns a function that sends each distinct error once
// per page load, and at most maxPerMinute errors a minute, so a render loop
// cannot flood the backend. It never throws: a failing report must not raise
// the error it was reporting again.
export function createErrorReporter(
	send: (report: ClientErrorReport) => Promise<unknown>,
	{ maxPerMinute = 5, now = Date.now }: { maxPerMinute?: number; now?: () => number } = {},
): (error: unknown, source: string) => void {
	const seen = new Set<string>()
	let windowStart = -Infinity
	let sentInWindow = 0

	return (error, source) => {
		try {
			const err = error instanceof Error ? error : new Error(String(error))
			const key = `${source}\n${err.message}`
			if (seen.has(key)) return
			if (now() - windowStart > 60_000) {
				windowStart = now()
				sentInWindow = 0
			}
			if (sentInWindow >= maxPerMinute) return
			seen.add(key)
			sentInWindow++
			send({
				message: err.message.slice(0, MAX_MESSAGE),
				stack: (err.stack ?? '').slice(0, MAX_STACK),
				source,
				// The path only: a query string can carry tokens.
				path: window.location.pathname,
			}).catch(() => {})
		} catch {
			// Reporting is best effort.
		}
	}
}

// installErrorReporting reports Vue render errors, uncaught errors and
// unhandled promise rejections to the backend.
export function installErrorReporting(app: App) {
	const report = createErrorReporter((body) =>
		// Plain fetch, not api: a 401 here must not trigger the session redirect.
		fetch(`${import.meta.env.VITE_API_URL || ''}/api/client-errors`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(body),
			keepalive: true,
		}),
	)
	app.config.errorHandler = (err) => {
		console.error(err)
		report(err, 'vue')
	}
	window.addEventListener('error', (e) => report(e.error ?? e.message, 'window'))
	window.addEventListener('unhandledrejection', (e) => report(e.reason, 'promise'))
}
