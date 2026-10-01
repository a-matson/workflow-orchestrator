import { describe, expect, it } from 'vitest'
import { freeNodePosition, type Point } from './useDAGLayout'

// A task node renders at most 200px wide and about 100px tall.
const overlaps = (a: Point, b: Point) => Math.abs(a.x - b.x) < 200 && Math.abs(a.y - b.y) < 100

describe('freeNodePosition', () => {
	it('never places a new node on top of an existing one', () => {
		// A row the user dragged to where the next node used to go.
		const occupied = [
			{ x: 120, y: 470 },
			{ x: 320, y: 470 },
			{ x: 520, y: 470 },
		]
		for (let i = 0; i < 20; i++) {
			const p = freeNodePosition(occupied)
			for (const o of occupied)
				expect(overlaps(p, o), `${JSON.stringify(p)} overlaps ${JSON.stringify(o)}`).toBe(false)
		}
	})

	it('fills a gap a deleted node left', () => {
		const occupied = [
			{ x: 120, y: 80 },
			{ x: 600, y: 80 },
		]
		const p = freeNodePosition(occupied)
		expect(occupied.some((o) => overlaps(p, o))).toBe(false)
		expect(p.y).toBe(80)
	})
})
