export interface Point {
	x: number
	y: number
}

// A slot is wider and taller than a task node (at most 200px wide, about
// 100px tall), so nodes in neighbouring slots never touch.
const SLOT = { x0: 120, y0: 80, dx: 240, dy: 140, columns: 3 }

// freeNodePosition is where the builder puts a new node: the first slot, row
// by row, that no existing node overlaps. Nodes the user dragged anywhere are
// avoided as well, because the check is against their actual positions.
export function freeNodePosition(occupied: Point[]): Point {
	const clear = (p: Point) =>
		occupied.every(
			(o) => Math.abs(o.x - p.x) >= SLOT.dx - 20 || Math.abs(o.y - p.y) >= SLOT.dy - 20,
		)
	for (let slot = 0; ; slot++) {
		const p = {
			x: SLOT.x0 + (slot % SLOT.columns) * SLOT.dx,
			y: SLOT.y0 + Math.floor(slot / SLOT.columns) * SLOT.dy,
		}
		if (clear(p)) return p
	}
}
