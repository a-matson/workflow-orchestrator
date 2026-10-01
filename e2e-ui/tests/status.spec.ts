import { expect, test } from './fixtures'

// UI-10: outcomes and states shown only by colour or by a silent banner are
// lost on a screen reader.
test('the validation result is announced as a status', async ({ page }) => {
	await page.goto('/builder')
	await page.getByTestId('validate-workflow').click()
	await expect(page.getByRole('status').filter({ hasText: 'Valid DAG' })).toBeVisible()
})

test('log level filters expose their on/off state', async ({ page, api }) => {
	const wf = await api.createWorkflow({
		name: 'e2e-ui-status',
		tasks: [{ id: 'noop', name: 'No-op', type: 'generic', dependencies: [] }],
	})
	const exec = await api.trigger(wf.id)
	await page.goto('/logs')
	await page.getByTestId(`log-exec-${exec.id}`).click()

	const error = page.getByRole('button', { name: 'error', exact: true })
	const pressed = await error.getAttribute('aria-pressed')
	expect(pressed, 'aria-pressed').toMatch(/^(true|false)$/)
	await error.click()
	await expect(error).toHaveAttribute('aria-pressed', pressed === 'true' ? 'false' : 'true')
})
