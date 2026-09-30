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

test('a deep link to a run older than the first page renders its detail', async ({ page, api }) => {
	const wf = await api.createWorkflow({ ...noop, name: 'e2e-ui-deep-link' })
	// The list shows 50 runs, so the first of 51 is only reachable by fetching it directly.
	const oldest = await api.trigger(wf.id)
	for (let i = 0; i < 50; i++) await api.trigger(wf.id)

	await page.goto(`/executions/${oldest.id}`)

	await expect(page.getByTestId('exec-detail-id')).toHaveText(oldest.id)
	await expect(page.getByTestId('exec-detail-name')).toHaveText('e2e-ui-deep-link')
	await expect(page.getByTestId('exec-detail-status')).toBeVisible()
	await expect(page.getByTestId('exec-detail-tasks')).toBeVisible()
})
