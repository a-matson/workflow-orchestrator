import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Metrics from './Metrics.vue'
import { useWorkflowStore } from '../stores/workflow'
import type { PlatformMetrics } from '../types'

vi.mock('../composables/useApi', () => ({ api: { get: vi.fn() } }))

const metrics = (over: Partial<PlatformMetrics>) =>
	({
		workflows_started: 0,
		workflows_completed: 0,
		workflows_failed: 0,
		workflows_cancelled: 0,
		tasks_dispatched: 0,
		tasks_completed: 0,
		tasks_retried: 0,
		tasks_dead_lettered: 0,
		...over,
	}) as PlatformMetrics

function kpi(label: string, m: PlatformMetrics) {
	const store = useWorkflowStore()
	store.metrics = m
	const w = mount(Metrics)
	return w.get(`[data-testid="kpi-${label}"] [data-testid="kpi-value"]`).text()
}

describe('Metrics page', () => {
	beforeEach(() => setActivePinia(createPinia()))

	it('shows a dash, not 100%, when no workflow has finished', () => {
		expect(kpi('Success rate', metrics({}))).toBe('—')
	})

	it('still computes the rate once workflows have finished', () => {
		expect(kpi('Success rate', metrics({ workflows_completed: 3, workflows_failed: 1 }))).toBe(
			'75%',
		)
	})

	it('lists cancelled workflows next to failed ones', () => {
		const store = useWorkflowStore()
		store.metrics = metrics({ workflows_failed: 1, workflows_cancelled: 2 })
		expect(mount(Metrics).get('[data-testid="kpi-Success rate"]').text()).toContain('2 cancelled')
	})

	it('leaves other numbers unchanged', () => {
		expect(kpi('Tasks dispatched', metrics({ tasks_dispatched: 7 }))).toBe('7')
	})
})
