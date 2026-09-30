import { apiKey, expect, signIn, test } from './fixtures'

test.use({ loggedIn: false })

test.describe('wrong key', () => {
	// The browser reports the rejected POST /api/session itself; that is the behaviour under test.
	test.use({ allowConsoleErrors: [/status of 401/] })

	test('shows the error and stays on /login', async ({ page }) => {
		await page.goto('/login?redirect=/metrics')
		await signIn(page, 'flx_not-a-real-key')
		await expect(page.getByTestId('login-error')).toBeVisible()
		await expect(page).toHaveURL(/\/login/)
	})
})

test('right key follows the redirect target', async ({ page }) => {
	await page.goto('/login?redirect=/metrics')
	await signIn(page, apiKey)
	await expect(page).toHaveURL(/\/metrics$/)
	await expect(page.getByTestId('session-principal')).toBeVisible()
})
