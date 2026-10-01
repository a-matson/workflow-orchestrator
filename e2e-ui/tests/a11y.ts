import AxeBuilder from '@axe-core/playwright'
import { expect, type Page } from '@playwright/test'
import { readFileSync, writeFileSync } from 'node:fs'

type Counts = Record<string, number>
const baselineFile = new URL('../a11y-baseline.json', import.meta.url)

// A ratchet, like the coverage baseline: a rule's node count may fall but never
// rise. `A11Y_RECORD=1 make e2e-ui` rewrites the baseline from the current UI.
export async function expectA11yBaseline(page: Page, name: string) {
	// color-contrast reads computed opacity, so a page still fading in would
	// count differently from run to run. Polled, because a route's enter
	// transition can start after the first check; looping animations never finish.
	// App.vue's route <Transition name="fade"> marks the entering page from its
	// first frame, before the CSS transition (and so getAnimations) starts.
	await expect(page.locator('.fade-enter-active, .fade-leave-active')).toHaveCount(0)
	await expect
		.poll(() =>
			page.evaluate(
				() =>
					document
						.getAnimations()
						.filter((a) => a.playState === 'running' && a.effect?.getTiming().iterations !== Infinity)
						.length,
			),
		)
		.toBe(0)
	const { violations } = await new AxeBuilder({ page }).analyze()
	const counts: Counts = Object.fromEntries(
		violations.map((v) => [v.id, v.nodes.length] as const).sort(([a], [b]) => a.localeCompare(b)),
	)
	const baseline: Record<string, Counts> = JSON.parse(readFileSync(baselineFile, 'utf8'))

	if (process.env.A11Y_RECORD) {
		baseline[name] = counts
		writeFileSync(baselineFile, JSON.stringify(baseline, null, 2) + '\n')
		return
	}

	const allowed = baseline[name] ?? {}
	const worse = Object.entries(counts)
		.filter(([rule, n]) => n > (allowed[rule] ?? 0))
		.map(([rule, n]) => {
			const nodes = violations.find((v) => v.id === rule)?.nodes.map((node) => node.target.join(' '))
			return `${rule}: ${allowed[rule] ?? 0} -> ${n} (${nodes?.join(', ')})`
		})
	const better = Object.entries(allowed).filter(([rule, n]) => (counts[rule] ?? 0) < n)
	if (better.length) {
		console.log(
			`a11y ${name}: ${better.map(([r, n]) => `${r} ${n} -> ${counts[r] ?? 0}`).join(', ')}; ` +
				'lower the baseline with `A11Y_RECORD=1 make e2e-ui`',
		)
	}
	expect(worse, `a11y violations above baseline on ${name}`).toEqual([])
}
