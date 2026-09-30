import { expect, test } from './fixtures'

test('leaving the builder with unsaved work asks first and cancelling keeps the canvas', async ({ page }) => {
	const prompts: string[] = []
	page.on('dialog', (dialog) => {
		prompts.push(dialog.message())
		void dialog.dismiss()
	})

	await page.goto('/builder')
	const nodes = page.locator('.vue-flow__node')
	const before = await nodes.count()
	await page.getByTestId('task-type-select').selectOption('generic')
	await page.getByTestId('add-task').click()
	await expect(nodes).toHaveCount(before + 1)

	await page.getByTestId('nav-executions').click()

	await expect.poll(() => prompts.length, { message: 'no leave confirmation was shown' }).toBe(1)
	await expect(page).toHaveURL(/\/builder$/)
	await expect(nodes).toHaveCount(before + 1)

	await page.getByTestId('new-workflow').click()

	await expect.poll(() => prompts.length, { message: 'New discarded work without asking' }).toBe(2)
	await expect(nodes).toHaveCount(before + 1)
})
