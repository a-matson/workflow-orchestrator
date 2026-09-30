import { expect, test } from './fixtures'

test('navigates across the four pages without console errors', async ({ page }) => {
	await page.goto('/')
	await expect(page).toHaveURL(/\/builder$/)
	for (const name of ['executions', 'logs', 'metrics', 'builder']) {
		await page.getByTestId(`nav-${name}`).click()
		await expect(page).toHaveURL(new RegExp(`/${name}$`))
		await expect(page.getByTestId(`nav-${name}`)).toHaveClass(/nav-link--active/)
	}
})
