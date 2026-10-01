import { readFile } from 'node:fs/promises'
import { expect, test } from './fixtures'

// UI-7: the chip used to open the presign endpoint's JSON; it must download
// the file itself through the task-scoped route.
test('an artifact chip downloads the file the task wrote', async ({ page, api }) => {
	const wf = await api.createWorkflow({
		name: 'e2e-ui-artifact',
		tasks: [
			{
				id: 'write',
				name: 'Write',
				type: 'generic',
				dependencies: [],
				config: { command: 'sh', args: ['-c', 'echo hello > /workspace/out.txt'] },
				container: { image: 'alpine:3.22' },
				artifacts_out: [{ path: 'out.txt' }],
			},
		],
	})
	const exec = await api.trigger(wf.id)

	await page.goto(`/executions/${exec.id}`)
	const status = page.getByTestId('task-status').first()
	// Generous: the first run may pull the image.
	await expect(status).toHaveText('completed', { timeout: 120_000 })
	await status.click()

	const chip = page.getByTestId('artifact-link')
	await expect(chip).toHaveAttribute('href', /^\/api\/tasks\/[^/]+\/artifacts\/out\.txt$/)
	await expect(chip).toHaveAttribute('download')
	const [download] = await Promise.all([page.waitForEvent('download'), chip.click()])
	expect(download.suggestedFilename()).toBe('out.txt')
	expect(await readFile(await download.path(), 'utf8')).toBe('hello\n')
})
