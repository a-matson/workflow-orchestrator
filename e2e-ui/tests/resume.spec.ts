import { expect, test } from './fixtures'

// F5: a cancelled run can be resumed in place, re-running its tasks.
test('a cancelled run resumes as the same run', async ({ page, api }) => {
	const wf = await api.createWorkflow({
		name: 'e2e-ui-resume',
		tasks: [
			{
				id: 'sleep',
				name: 'Sleep',
				type: 'generic',
				dependencies: [],
				config: { command: 'sh', args: ['-c', 'sleep 30'] },
				container: { image: 'alpine:3.22' },
			},
		],
	})
	const exec = await api.trigger(wf.id)
	await page.goto(`/executions/${exec.id}`)
	await page.getByTestId('cancel-exec').click()
	await expect(page.getByTestId('exec-detail-status')).toHaveText('cancelled')

	await page.getByTestId('resume-exec').click()
	await expect(page.getByTestId('exec-detail-status')).toHaveText('running')
	await expect(page.getByTestId('exec-detail-id')).toHaveText(exec.id)
	await expect(page.getByTestId('task-status').first()).not.toHaveText('cancelled')

	// Leave no container running for later specs.
	await page.getByTestId('cancel-exec').click()
	await expect(page.getByTestId('exec-detail-status')).toHaveText('cancelled')
})
