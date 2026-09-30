import { test as base, expect, type Page } from '@playwright/test'
import path from 'node:path'

// `make e2e-ui` mints this key per run and installs it as the stack's bootstrap admin key.
export const apiKey = process.env.FLUXOR_API_KEY ?? ''
const apiURL = process.env.FLUXOR_API_URL ?? 'http://localhost:8080'

// Console errors every test tolerates. Starts empty on purpose: an entry needs
// a linked issue, because each one hides a real bug from every spec.
const consoleAllowlist: RegExp[] = []

export async function signIn(page: Page, key: string) {
	await page.getByTestId('login-key').fill(key)
	await page.getByTestId('login-submit').click()
}

export interface Api {
	createWorkflow(def: object): Promise<{ id: string }>
	trigger(workflowId: string): Promise<{ id: string }>
}

interface Fixtures {
	loggedIn: boolean
	// Per-test allowlist for errors the test itself provokes, such as a 401 from a wrong key.
	allowConsoleErrors: RegExp[]
	failOnConsoleErrors: void
	api: Api
}

export const test = base.extend<Fixtures, { workerStorageState: string }>({
	loggedIn: [true, { option: true }],
	allowConsoleErrors: [[], { option: true }],

	// Signs in through the real login page once per worker, so every spec
	// exercises the cookie session the browser actually gets.
	workerStorageState: [
		async ({ browser }, use, workerInfo) => {
			const file = path.join(workerInfo.project.outputDir, `.auth/worker-${workerInfo.parallelIndex}.json`)
			const context = await browser.newContext({ baseURL: workerInfo.project.use.baseURL })
			const page = await context.newPage()
			await page.goto('/login')
			await signIn(page, apiKey)
			await expect(page.getByTestId('session-principal')).toBeVisible()
			await context.storageState({ path: file })
			await context.close()
			await use(file)
		},
		{ scope: 'worker' },
	],

	storageState: async ({ loggedIn, workerStorageState }, use) => {
		await use(loggedIn ? workerStorageState : { cookies: [], origins: [] })
	},

	failOnConsoleErrors: [
		async ({ page, allowConsoleErrors }, use) => {
			const errors: string[] = []
			page.on('console', (msg) => {
				if (msg.type() === 'error') errors.push(`console: ${msg.text()} (${msg.location().url})`)
			})
			page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
			await use()
			const allowed = [...consoleAllowlist, ...allowConsoleErrors]
			expect(errors.filter((e) => !allowed.some((re) => re.test(e)))).toEqual([])
		},
		{ auto: true },
	],

	// Setup only: specs assert on what the UI shows, never on these responses.
	api: async ({ playwright }, use) => {
		const request = await playwright.request.newContext({
			baseURL: apiURL,
			extraHTTPHeaders: { Authorization: `Bearer ${apiKey}` },
		})
		const post = async (url: string, data: object, status: number) => {
			const res = await request.post(url, { data })
			expect(res.status(), `POST ${url}: ${await res.text()}`).toBe(status)
			return res.json()
		}
		await use({
			createWorkflow: (def) => post('/api/workflows', def, 201),
			trigger: (id) => post(`/api/workflows/${id}/trigger`, {}, 202),
		})
		await request.dispose()
	},
})

export { expect }
