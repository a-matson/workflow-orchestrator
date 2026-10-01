import { expect, test } from './fixtures'

const viewports = [
	{ width: 1440, height: 900 },
	{ width: 1024, height: 768 },
	// UI-16: a phone-sized window clipped the toolbar, so Save was off-screen.
	{ width: 390, height: 844 },
]

for (const viewport of viewports) {
	test(`task panel leaves the toolbar reachable at ${viewport.width}x${viewport.height}`, async ({ page }) => {
		await page.setViewportSize(viewport)
		await page.goto('/builder')
		await page.getByTestId('task-type-select').selectOption('generic')
		await page.getByTestId('add-task').click()
		await expect(page.getByTestId('config-panel')).toBeVisible()

		for (const id of ['auto-layout', 'validate-workflow', 'save-workflow']) {
			const button = page.getByTestId(id)
			const hitsButton = await button.evaluate((el) => {
				const r = el.getBoundingClientRect()
				const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
				return el.contains(hit)
			})
			expect(hitsButton, `${id} is under another element`).toBe(true)
			// Actionability checks fail when another element intercepts the click.
			await button.click({ timeout: 3_000 })
		}
	})
}
