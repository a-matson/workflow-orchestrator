import { expectA11yBaseline } from './a11y'
import { expect, test } from './fixtures'

for (const path of ['/builder', '/executions', '/logs', '/metrics']) {
	test(`axe ${path}`, async ({ page }) => {
		await page.goto(path)
		await expect(page.getByTestId('session-principal')).toBeVisible()
		await expectA11yBaseline(page, path)
	})
}

test('axe /builder with the task panel open', async ({ page }) => {
	await page.goto('/builder')
	await page.getByTestId('task-type-select').selectOption('generic')
	await page.getByTestId('add-task').click()
	// A new node lands at a random x; a fixed layout keeps the contrast count stable.
	await page.getByTestId('auto-layout').click()
	await expect(page.getByTestId('config-panel')).toBeVisible()
	await expectA11yBaseline(page, '/builder (panel open)')
})

test.describe('logged out', () => {
	test.use({ loggedIn: false })

	test('axe /login', async ({ page }) => {
		await page.goto('/login')
		await expect(page.getByTestId('login-form')).toBeVisible()
		await expectA11yBaseline(page, '/login')
	})
})
