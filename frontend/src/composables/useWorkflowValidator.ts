import { computed } from 'vue'
import type { TaskDefinition } from '../types'

export interface ValidationIssue {
	type: 'error' | 'warning'
	code: string
	message: string
	taskId?: string
}

export interface ValidationResult {
	valid: boolean
	issues: ValidationIssue[]
	stats: {
		taskCount: number
		edgeCount: number
		maxDepth: number
		rootCount: number
		leafCount: number
		isolatedCount: number
	}
}

/**
 * Client-side workflow DAG validator.
 * Mirrors the Go DAG validation logic so users get instant feedback in the builder.
 */
export function useWorkflowValidator(tasks: () => TaskDefinition[]) {
	const result = computed<ValidationResult>(() => validateDAG(tasks()))
	return result
}

export function validateDAG(tasks: TaskDefinition[]): ValidationResult {
	const issues: ValidationIssue[] = []
	const taskMap = new Map(tasks.map((t) => [t.id, t]))

	// ── 1. Duplicate IDs ─────────────────────────────────────────
	const idCounts = new Map<string, number>()
	for (const t of tasks) {
		idCounts.set(t.id, (idCounts.get(t.id) ?? 0) + 1)
	}
	idCounts.forEach((count, id) => {
		if (count > 1) {
			issues.push({
				type: 'error',
				code: 'DUPLICATE_ID',
				message: `Duplicate task ID: "${id}"`,
				taskId: id,
			})
		}
	})

	// ── 2. Unknown dependencies ──────────────────────────────────
	for (const t of tasks) {
		for (const dep of t.dependencies ?? []) {
			if (!taskMap.has(dep)) {
				issues.push({
					type: 'error',
					code: 'UNKNOWN_DEP',
					message: `Task "${t.name}" depends on unknown task "${dep}"`,
					taskId: t.id,
				})
			}
		}
	}

	// ── 3. Self-dependencies ─────────────────────────────────────
	for (const t of tasks) {
		if ((t.dependencies ?? []).includes(t.id)) {
			issues.push({
				type: 'error',
				code: 'SELF_DEP',
				message: `Task "${t.name}" depends on itself`,
				taskId: t.id,
			})
		}
	}

	// ── 4. Cycle detection (DFS) ─────────────────────────────────
	const visited = new Set<string>()
	const inStack = new Set<string>()
	const cycleNodes = new Set<string>()

	function dfs(id: string): boolean {
		visited.add(id)
		inStack.add(id)
		const task = taskMap.get(id)
		for (const dep of task?.dependencies ?? []) {
			if (!taskMap.has(dep)) continue
			if (!visited.has(dep) && dfs(dep)) return true
			if (inStack.has(dep)) {
				cycleNodes.add(id)
				cycleNodes.add(dep)
				return true
			}
		}
		inStack.delete(id)
		return false
	}

	for (const t of tasks) {
		if (!visited.has(t.id)) dfs(t.id)
	}

	if (cycleNodes.size > 0) {
		issues.push({
			type: 'error',
			code: 'CYCLE',
			message: `Cycle detected involving tasks: ${[...cycleNodes].join(', ')}`,
		})
	}

	// ── 5. Empty workflow ────────────────────────────────────────
	if (tasks.length === 0) {
		issues.push({ type: 'warning', code: 'EMPTY', message: 'Workflow has no tasks' })
	}

	// ── 6. Missing task names ────────────────────────────────────
	for (const t of tasks) {
		if (!t.name?.trim()) {
			issues.push({
				type: 'warning',
				code: 'NO_NAME',
				message: `Task "${t.id}" has no name`,
				taskId: t.id,
			})
		}
	}

	// ── 7. Missing task types ────────────────────────────────────
	for (const t of tasks) {
		if (!t.type?.trim()) {
			issues.push({
				type: 'warning',
				code: 'NO_TYPE',
				message: `Task "${t.name || t.id}" has no type`,
				taskId: t.id,
			})
		}
	}

	// ── Stats ────────────────────────────────────────────────────
	const inDegree = new Map<string, number>()
	const outDegree = new Map<string, number>()
	tasks.forEach((t) => {
		inDegree.set(t.id, 0)
		outDegree.set(t.id, 0)
	})
	tasks.forEach((t) => {
		for (const dep of t.dependencies ?? []) {
			if (taskMap.has(dep)) {
				inDegree.set(t.id, (inDegree.get(t.id) ?? 0) + 1)
				outDegree.set(dep, (outDegree.get(dep) ?? 0) + 1)
			}
		}
	})

	const rootCount = [...inDegree.values()].filter((d) => d === 0).length
	const leafCount = [...outDegree.values()].filter((d) => d === 0).length
	const isolatedCount = tasks.filter(
		(t) => (inDegree.get(t.id) ?? 0) === 0 && (outDegree.get(t.id) ?? 0) === 0,
	).length
	const edgeCount = tasks.reduce((sum, t) => sum + (t.dependencies?.length ?? 0), 0)

	// Max depth via topological sort
	const levels = new Map<string, number>()
	function getLevel(id: string): number {
		if (levels.has(id)) return levels.get(id)!
		const t = taskMap.get(id)
		const deps = (t?.dependencies ?? []).filter((d) => taskMap.has(d))
		const level = deps.length === 0 ? 0 : Math.max(...deps.map((d) => getLevel(d) + 1))
		levels.set(id, level)
		return level
	}
	tasks.forEach((t) => getLevel(t.id))
	const maxDepth = tasks.length > 0 ? Math.max(...[...levels.values()]) : 0

	// ── 8. Warnings ──────────────────────────────────────────────
	if (isolatedCount > 0 && tasks.length > 1) {
		issues.push({
			type: 'warning',
			code: 'ISOLATED',
			message: `${isolatedCount} task(s) have no dependencies or dependents`,
		})
	}

	if (maxDepth > 20) {
		issues.push({
			type: 'warning',
			code: 'DEEP_CHAIN',
			message: `Workflow has a very deep dependency chain (${maxDepth} levels) — consider parallelising`,
		})
	}

	return {
		valid: issues.filter((i) => i.type === 'error').length === 0,
		issues,
		stats: { taskCount: tasks.length, edgeCount, maxDepth, rootCount, leafCount, isolatedCount },
	}
}
