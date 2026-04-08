import type { TaskDefinition } from '../types'

export interface LayoutNode {
  id: string
  x: number
  y: number
  level: number
  column: number
}

interface LayoutOptions {
  nodeWidth?: number
  nodeHeight?: number
  levelGap?: number // vertical space between levels
  colGap?: number // horizontal space between siblings
  startX?: number
  startY?: number
}

const DEFAULTS: Required<LayoutOptions> = {
  nodeWidth: 180,
  nodeHeight: 50,
  levelGap: 130,
  colGap: 210,
  startX: 60,
  startY: 60,
}

/**
 * Layered DAG layout using a simplified Sugiyama approach:
 * 1. Assign levels via longest-path from roots (critical-path depth)
 * 2. Sort nodes within each level to minimize edge crossings
 * 3. Center each level horizontally
 *
 * Returns a map of taskId → { x, y, level, column }
 */
export function useDAGLayout(
  tasks: TaskDefinition[],
  opts: LayoutOptions = {},
): Map<string, LayoutNode> {
  const cfg = { ...DEFAULTS, ...opts }
  const result = new Map<string, LayoutNode>()

  if (!tasks.length) return result

  // ── Step 1: Compute levels (longest path from source) ────────
  const levelMap = new Map<string, number>()
  const taskMap = new Map(tasks.map((t) => [t.id, t]))

  function getLevel(id: string): number {
    if (levelMap.has(id)) return levelMap.get(id)!
    const task = taskMap.get(id)
    if (!task || !task.dependencies.length) {
      levelMap.set(id, 0)
      return 0
    }
    const level = Math.max(...task.dependencies.map((d) => getLevel(d) + 1))
    levelMap.set(id, level)
    return level
  }
  tasks.forEach((t) => getLevel(t.id))

  // ── Step 2: Group by level ────────────────────────────────────
  const byLevel = new Map<number, string[]>()
  levelMap.forEach((lv, id) => {
    if (!byLevel.has(lv)) byLevel.set(lv, [])
    byLevel.get(lv)!.push(id)
  })

  // ── Step 3: Sort each level to minimize crossings ─────────────
  // Simple barycenter heuristic: sort by average x-position of parents
  byLevel.forEach((ids, level) => {
    if (level === 0) return // root level: no sorting needed
    ids.sort((a, b) => {
      const aParents = taskMap.get(a)?.dependencies ?? []
      const bParents = taskMap.get(b)?.dependencies ?? []
      const avgX = (parentIds: string[]) => {
        if (!parentIds.length) return 0
        const parentLevelIds = byLevel.get(level - 1) ?? []
        const positions = parentIds.map((p) => parentLevelIds.indexOf(p)).filter((i) => i >= 0)
        return positions.length ? positions.reduce((s, v) => s + v, 0) / positions.length : 0
      }
      return avgX(aParents) - avgX(bParents)
    })
  })

  // ── Step 4: Assign pixel positions ───────────────────────────
  const maxLevel = Math.max(...levelMap.values())

  byLevel.forEach((ids, level) => {
    const totalWidth = ids.length * cfg.colGap
    const offsetX = cfg.startX - totalWidth / 2 + cfg.colGap / 2

    ids.forEach((id, col) => {
      result.set(id, {
        id,
        x: Math.max(cfg.startX, offsetX + col * cfg.colGap),
        y: cfg.startY + level * cfg.levelGap,
        level,
        column: col,
      })
    })
  })

  // Centre the whole graph around 450px if it has multiple levels
  if (maxLevel > 0) {
    const xs = [...result.values()].map((n) => n.x)
    const centreX = (Math.min(...xs) + Math.max(...xs)) / 2
    const shift = 450 - centreX
    result.forEach((node) => {
      node.x += shift
    })
  }

  return result
}

/**
 * Convert DAG tasks + layout into VueFlow-compatible nodes.
 */
export function tasksToFlowNodes(
  tasks: TaskDefinition[],
  layout: Map<string, LayoutNode>,
  statusMap: Map<string, string> = new Map(),
) {
  return tasks.map((t) => {
    const pos = layout.get(t.id) ?? { x: 100, y: 100 }
    return {
      id: t.id,
      type: 'taskNode' as const,
      position: { x: pos.x, y: pos.y },
      data: {
        taskDef: t,
        status: statusMap.get(t.id) ?? 'pending',
      },
    }
  })
}

/**
 * Convert DAG tasks into VueFlow-compatible edges.
 */
export function tasksToFlowEdges(
  tasks: TaskDefinition[],
  statusMap: Map<string, string> = new Map(),
) {
  return tasks.flatMap((t) =>
    (t.dependencies ?? []).map((depId) => {
      const targetStatus = statusMap.get(t.id) ?? 'pending'
      const isRunning = targetStatus === 'running'
      return {
        id: `e-${depId}-${t.id}`,
        source: depId,
        target: t.id,
        type: 'smoothstep' as const,
        animated: isRunning,
        style: {
          stroke: isRunning ? '#3b9eff' : '#2e2e48',
          strokeWidth: '1.5',
        },
      }
    }),
  )
}
