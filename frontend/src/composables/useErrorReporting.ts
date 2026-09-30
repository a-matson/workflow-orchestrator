export interface ClientErrorReport {
	message: string
	stack: string
	source: string
	path: string
}

// Stub for the red test.
export function createErrorReporter(
	_send: (report: ClientErrorReport) => Promise<unknown>,
	_opts: { maxPerMinute?: number; now?: () => number } = {},
): (error: unknown, source: string) => void {
	return () => {}
}
