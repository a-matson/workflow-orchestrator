import { expect, test } from './fixtures'

// UI-10: rows that open something were click-only divs, so a keyboard user
// could not open a run, a task, a workflow or a run's logs.
const noop = {
	name: 'e2e-ui-keyboard',
	tasks: [{ id: 'noop', name: 'No-op', type: 'generic', dependencies: [] }],
}

test('a run and its task open from the keyboard', async ({ page, api }) => {
	const wf = await api.createWorkflow(noop)
	const exec = await api.trigger(wf.id)
	await page.goto('/executions')

	const row = page.getByTestId(`exec-${exec.id}`)
	await row.focus()
	await expect(row).toBeFocused()
	await page.keyboard.press('Enter')
	await expect(page.getByTestId('exec-detail-id')).toHaveText(exec.id)

	const task = page.getByTestId('task-status').first().locator('..')
	await task.focus()
	await expect(task).toBeFocused()
	await page.keyboard.press(' ')
	await expect(page.getByTestId('task-detail')).toBeVisible()
})

test('a saved workflow opens from the keyboard', async ({ page, api }) => {
	await api.createWorkflow({ ...noop, name: 'e2e-ui-keyboard-open' })
	await page.goto('/builder')

	const item = page.getByTestId('wf-item').filter({ hasText: 'e2e-ui-keyboard-open' }).first()
	await item.focus()
	await expect(item).toBeFocused()
	await page.keyboard.press('Enter')
	await expect(page.getByPlaceholder('Workflow name…')).toHaveValue('e2e-ui-keyboard-open')
})

test("a run's logs open from the keyboard", async ({ page, api }) => {
	const wf = await api.createWorkflow({ ...noop, name: 'e2e-ui-keyboard-logs' })
	const exec = await api.trigger(wf.id)
	await page.goto('/logs')

	const item = page.getByTestId(`log-exec-${exec.id}`)
	await item.focus()
	await expect(item).toBeFocused()
	await page.keyboard.press('Enter')
	await expect(item).toHaveClass(/active/)
})
