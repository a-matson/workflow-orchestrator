import { expect, test } from './fixtures'

const yaml = `name: e2e-ui-yaml
max_parallel: 1
tasks:
  - id: only
    name: Only
    type: generic
    dependencies: []
    timeout: 30s
`

test('a YAML workflow imports from the builder and offers an export', async ({ page }) => {
	await page.goto('/builder')
	await page.getByTestId('import-workflow-file').setInputFiles({
		name: 'wf.yaml',
		mimeType: 'application/yaml',
		buffer: Buffer.from(yaml),
	})

	await expect(page.getByTestId('wf-item').filter({ hasText: 'e2e-ui-yaml' })).toBeVisible()
	await expect(page.getByPlaceholder('Workflow name…')).toHaveValue('e2e-ui-yaml')
	await expect(page.getByTestId('export-workflow')).toHaveAttribute('href', /^\/api\/workflows\/[^/]+\/export$/)

	const [download] = await Promise.all([page.waitForEvent('download'), page.getByTestId('export-workflow').click()])
	expect(download.suggestedFilename()).toBe('e2e-ui-yaml.yaml')
})
