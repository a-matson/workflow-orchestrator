import { expect, test } from './fixtures'

// A generic task without a command is a no-op in the worker, so this needs no container runtime.
const noop = {
	name: 'e2e-ui-noop',
	tasks: [{ id: 'noop', name: 'No-op', type: 'generic', dependencies: [] }],
}

test('a triggered run appears and reaches completed without a reload', async ({ page, api }) => {
	await page.goto('/executions')
	// Triggering before the socket is open would let the initial fetch, not the live update, show the run.
	await expect(page.getByTestId('ws-status')).toHaveClass(/connected/)

	const wf = await api.createWorkflow(noop)
	const exec = await api.trigger(wf.id)

	const row = page.getByTestId(`exec-${exec.id}`)
	await expect(row.getByTestId('exec-status')).toHaveText('completed', { timeout: 60_000 })
})
