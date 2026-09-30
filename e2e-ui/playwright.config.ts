import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
	testDir: './tests',
	forbidOnly: !!process.env.CI,
	// No retries: a flaky UI test is a finding to fix, not noise to hide.
	retries: 0,
	reporter: [['list'], ['html', { open: 'never' }]],
	use: {
		baseURL: process.env.FLUXOR_UI_URL ?? 'http://localhost:3000',
		trace: 'retain-on-failure',
	},
	projects: [
		// Runs first, against the empty stack `make e2e-ui` brings up: the axe
		// counts include list rows, so data created by other specs would move them.
		{ name: 'a11y', testMatch: 'a11y.spec.ts', use: { ...devices['Desktop Chrome'] } },
		{
			name: 'flows',
			testIgnore: 'a11y.spec.ts',
			dependencies: ['a11y'],
			use: { ...devices['Desktop Chrome'] },
		},
	],
})
