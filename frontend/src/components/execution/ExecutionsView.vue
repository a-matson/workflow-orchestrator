<template>
	<div class="executions-view">
		<!-- ── Left: Run list ──────────────────────────────── -->
		<div class="exec-list-panel">
			<div class="list-header">
				<h2 class="list-title">Runs</h2>
				<div class="list-header-actions">
					<select v-model="statusFilter" class="filter-select">
						<option value="">All statuses</option>
						<option value="running">Running</option>
						<option value="completed">Completed</option>
						<option value="failed">Failed</option>
					</select>
				</div>
			</div>

			<div ref="listScroll" class="list-scroll">
				<div v-if="!filteredExecs.length" class="list-empty">
					<div class="list-empty-icon">⬡</div>
					<div>No executions yet</div>
				</div>

				<TransitionGroup name="exec-item" tag="div">
					<div
						v-for="exec in filteredExecs"
						:key="exec.id"
						class="exec-item"
						:class="{ active: exec.id === selectedId }"
						@click="$emit('select', exec.id)"
					>
						<div class="exec-item-row1">
							<StatusBadge :status="exec.status" />
							<span class="exec-wf-name">{{ exec.workflow_name }}</span>
							<span class="exec-elapsed">{{ elapsed(exec) }}</span>
						</div>

						<div class="exec-item-row2">
							<div class="mini-progress">
								<div
									class="mini-progress-fill"
									:style="{ width: completePct(exec) + '%', background: statusColor(exec.status) }"
								></div>
							</div>
							<span class="exec-task-frac"
								>{{ completedTasks(exec) }}/{{ exec.tasks?.length ?? 0 }}</span
							>
						</div>

						<div class="exec-item-row3">
							<span class="exec-id-mono">{{ exec.id.slice(0, 12) }}…</span>
							<span class="exec-date">{{ formatDate(exec.created_at) }}</span>
						</div>
					</div>
				</TransitionGroup>
			</div>
		</div>

		<!-- ── Right: Detail ──────────────────────────────── -->
		<div class="exec-detail-panel">
			<div v-if="!selectedExec" class="detail-empty">
				<div class="detail-empty-icon">◈</div>
				<div class="detail-empty-text">Select an execution</div>
			</div>

			<template v-else>
				<!-- Header -->
				<div class="detail-header">
					<div class="detail-header-left">
						<StatusBadge :status="selectedExec.status" />
						<div>
							<div class="detail-wf-name">
								{{ selectedExec.workflow_name }}
							</div>
							<div class="detail-exec-id">
								{{ selectedExec.id }}
							</div>
						</div>
					</div>
					<div class="detail-header-right">
						<button
							v-if="selectedExec.status === 'failed' || selectedExec.status === 'completed'"
							class="hdr-btn retry"
							@click="$emit('retry', selectedExec.id)"
						>
							↺ Retry
						</button>
						<span class="detail-elapsed">{{ elapsed(selectedExec) }}</span>
					</div>
				</div>

				<!-- Stats row -->
				<div class="stats-row">
					<div v-for="s in taskStats(selectedExec)" :key="s.label" class="stat-cell">
						<div class="stat-val" :style="{ color: s.color }">
							{{ s.count }}
						</div>
						<div class="stat-lbl">
							{{ s.label }}
						</div>
					</div>
					<div class="stat-cell">
						<div class="stat-val">{{ completePct(selectedExec) }}%</div>
						<div class="stat-lbl">Progress</div>
					</div>
				</div>

				<!-- Progress bar -->
				<div class="detail-progress-track">
					<div
						class="detail-progress-fill"
						:style="{
							width: completePct(selectedExec) + '%',
							background: statusColor(selectedExec.status),
						}"
					></div>
				</div>

				<!-- DAG visualization -->
				<div ref="dagContainer" class="detail-dag">
					<svg
						:viewBox="`0 0 ${dagViewBox.w} ${dagViewBox.h}`"
						width="100%"
						height="100%"
						class="dag-svg"
					>
						<defs>
							<marker
								id="dv-arrow"
								markerWidth="7"
								markerHeight="5"
								refX="6"
								refY="2.5"
								orient="auto"
							>
								<polygon points="0 0,7 2.5,0 5" :fill="COLORS.edge" />
							</marker>
						</defs>

						<!-- Edges -->
						<path
							v-for="edge in dagEdges"
							:key="edge.id"
							:d="edge.d"
							fill="none"
							:stroke="edge.color"
							stroke-width="1.5"
							:stroke-dasharray="edge.animated ? '5 3' : '0'"
							marker-end="url(#dv-arrow)"
						>
							<animate
								v-if="edge.animated"
								attributeName="stroke-dashoffset"
								from="0"
								to="-16"
								dur="0.55s"
								repeatCount="indefinite"
							/>
						</path>

						<!-- Nodes -->
						<g
							v-for="node in dagNodePositions"
							:key="node.id"
							class="dag-node-g"
							:class="{ selected: node.id === selectedTaskDefId }"
							@click="selectedTaskDefId = selectedTaskDefId === node.id ? null : node.id"
						>
							<!-- Pulse ring (running) -->
							<rect
								v-if="node.status === 'running'"
								:x="node.x - 2"
								:y="node.y - 2"
								:width="NODE_W + 4"
								:height="NODE_H + 4"
								rx="11"
								fill="none"
								:stroke="node.color"
								stroke-width="1.5"
								style="animation: dagPulse 1.4s ease-in-out infinite"
							/>
							<!-- Body -->
							<rect
								:x="node.x"
								:y="node.y"
								:width="NODE_W"
								:height="NODE_H"
								rx="9"
								fill="#1a1a2e"
								:stroke="node.color"
								stroke-width="1"
							/>
							<!-- Status bar -->
							<rect
								:x="node.x + 1"
								:y="node.y + 1"
								:width="NODE_W - 2"
								height="3"
								:fill="node.color"
								rx="8"
							/>
							<!-- Icon -->
							<text :x="node.x + 10" :y="node.y + 27" font-size="13" dominant-baseline="middle">
								{{ node.icon }}
							</text>
							<!-- Name -->
							<text
								:x="node.x + 30"
								:y="node.y + 20"
								fill="#e8e8f5"
								font-size="10.5"
								font-weight="600"
								font-family="DM Sans, sans-serif"
							>
								{{ node.label }}
							</text>
							<!-- Status text -->
							<text
								:x="node.x + 30"
								:y="node.y + 35"
								:fill="node.color"
								font-size="8.5"
								font-family="IBM Plex Mono, monospace"
							>
								{{ node.status.toUpperCase() }}
							</text>
							<!-- Retry badge -->
							<g v-if="node.retryCount > 0">
								<rect
									:x="node.x + NODE_W - 30"
									:y="node.y + 8"
									width="24"
									height="14"
									rx="4"
									fill="rgba(245,166,35,0.18)"
								/>
								<text
									:x="node.x + NODE_W - 18"
									:y="node.y + 17"
									fill="#f5a623"
									font-size="9"
									font-family="IBM Plex Mono,monospace"
									text-anchor="middle"
								>
									↺{{ node.retryCount }}
								</text>
							</g>
						</g>
					</svg>

					<style>
						@keyframes dagPulse {
							0%,
							100% {
								stroke-opacity: 0.4;
							}
							50% {
								stroke-opacity: 0.95;
							}
						}
						.dag-node-g {
							cursor: pointer;
						}
						.dag-node-g.selected rect:nth-child(2) {
							stroke-width: 2 !important;
						}
					</style>
				</div>

				<!-- Task list -->
				<div ref="taskListEl" class="task-list">
					<div
						v-for="t in selectedExec.tasks"
						:key="t.id"
						class="task-row"
						:class="{ 'task-row-selected': t.task_definition_id === selectedTaskDefId }"
						@click="
							selectedTaskDefId =
								selectedTaskDefId === t.task_definition_id ? null : t.task_definition_id
						"
					>
						<StatusBadge :status="t.status" />
						<span class="task-name">{{ t.task_name }}</span>
						<span class="task-type-tag">{{ t.task_type }}</span>
						<span class="task-worker">{{ t.worker_id?.slice(-8) || '' }}</span>
						<span class="task-duration">{{ taskDuration(t) }}</span>
						<span v-if="t.retry_count > 0" class="task-retry">↺{{ t.retry_count }}</span>
					</div>

					<!-- Expanded task detail -->
					<Transition name="expand">
						<div v-if="expandedTask" class="task-detail">
							<div class="task-detail-grid">
								<span class="td-key">Worker</span>
								<span class="td-val">{{ expandedTask.worker_id || '—' }}</span>
								<span class="td-key">Started</span>
								<span class="td-val">{{
									expandedTask.started_at
										? new Date(expandedTask.started_at).toLocaleTimeString()
										: '—'
								}}</span>
								<span class="td-key">Duration</span>
								<span class="td-val">{{ taskDuration(expandedTask) }}</span>
								<span class="td-key">Retries</span>
								<span class="td-val"
									>{{ expandedTask.retry_count }} / {{ expandedTask.max_retries }}</span
								>
							</div>
							<div v-if="expandedTask.error" class="task-error">
								{{ expandedTask.error }}
							</div>
							<div v-if="expandedTask.logs?.length" class="task-mini-logs">
								<div
									v-for="(log, i) in expandedTask.logs.slice(-6)"
									:key="i"
									class="mini-log"
									:class="log.level"
								>
									<span class="ml-ts">{{ new Date(log.timestamp).toLocaleTimeString() }}</span>
									<span class="ml-level">{{ log.level?.toUpperCase() }}</span>
									<span class="ml-msg">{{ log.message }}</span>
								</div>
							</div>
						</div>
					</Transition>
				</div>
			</template>
		</div>
	</div>
</template>

<script setup lang="ts">
	import { ref, computed, watch } from 'vue'
	import StatusBadge from '../shared/StatusBadge.vue'
	import type { WorkflowExecution, TaskExecution } from '../../types'
	import { STATUS_COLORS, TASK_TYPES } from '../../types'
	import { useNow } from '@/composables/useNow'

	const props = defineProps<{
		executions: WorkflowExecution[]
		selectedId?: string | null
	}>()

	defineEmits<{ (e: 'select', id: string): void; (e: 'retry', id: string): void }>()

	// ── Constants ─────────────────────────────────────────────
	const NODE_W = 155
	const NODE_H = 46
	const COL_GAP = 190
	const ROW_GAP = 110
	const COLORS = { edge: '#3a3a5a' }

	// ── State ─────────────────────────────────────────────────
	const statusFilter = ref('')
	const selectedTaskDefId = ref<string | null>(null)
	const now = useNow()

	// ── Computed: exec list ───────────────────────────────────
	const filteredExecs = computed(() =>
		props.executions.filter((e) => !statusFilter.value || e.status === statusFilter.value),
	)

	const selectedExec = computed(
		() => props.executions.find((e) => e.id === props.selectedId) ?? null,
	)

	const expandedTask = computed(
		() =>
			selectedExec.value?.tasks.find((t) => t.task_definition_id === selectedTaskDefId.value) ??
			null,
	)

	// ── DAG layout ────────────────────────────────────────────
	const dagNodePositions = computed(() => {
		const exec = selectedExec.value
		if (!exec?.tasks?.length) return []

		// Build dependency map from task executions
		// (we use task_definition_id as node ID and approximate deps from task order)
		const tasks = exec.tasks
		const levels = new Map<string, number>()

		// Approximate: no dep info in TaskExecution, so we use order heuristic
		// (In practice, wire to workflowDef.tasks for real deps)
		tasks.forEach((t, i) => {
			levels.set(t.task_definition_id, Math.floor(i / 3))
		})

		const byLevel = new Map<number, typeof tasks>()
		tasks.forEach((t) => {
			const lv = levels.get(t.task_definition_id) ?? 0
			if (!byLevel.has(lv)) byLevel.set(lv, [])
			byLevel.get(lv)!.push(t)
		})

		const positions: {
			id: string
			x: number
			y: number
			label: string
			icon: string
			status: string
			color: string
			retryCount: number
		}[] = []

		byLevel.forEach((lvTasks, level) => {
			const totalW = lvTasks.length * COL_GAP
			lvTasks.forEach((t, i) => {
				const typeInfo = TASK_TYPES.find((ti) => ti.value === t.task_type) as
					| ((typeof TASK_TYPES)[number] & { icon?: string })
					| undefined
				const label = t.task_name.length > 14 ? t.task_name.slice(0, 13) + '…' : t.task_name
				positions.push({
					id: t.task_definition_id,
					x: 20 + i * COL_GAP + (Math.max(600, totalW) - totalW) / 2,
					y: 20 + level * ROW_GAP,
					label,
					icon: typeInfo?.icon ?? '⚙️',
					status: t.status,
					color: STATUS_COLORS[t.status] ?? '#9090a8',
					retryCount: t.retry_count,
				})
			})
		})
		return positions
	})

	const dagViewBox = computed(() => {
		if (!dagNodePositions.value.length) return { w: 600, h: 200 }
		const maxX = Math.max(...dagNodePositions.value.map((n) => n.x + NODE_W))
		const maxY = Math.max(...dagNodePositions.value.map((n) => n.y + NODE_H))
		return { w: Math.max(600, maxX + 30), h: maxY + 30 }
	})

	const dagEdges = computed(() => {
		// Since TaskExecution doesn't carry deps, draw edges level-to-level
		const edges: Array<{ id: string; d: string; color: string; animated: boolean }> = []
		// const byId = new Map(dagNodePositions.value.map((n) => [n.id, n]))
		const exec = selectedExec.value
		if (!exec) return edges

		// Group by level (approximated)
		const byLevel = new Map<number, typeof dagNodePositions.value>()
		dagNodePositions.value.forEach((n) => {
			const lv = Math.floor(exec.tasks.findIndex((t) => t.task_definition_id === n.id) / 3)
			if (!byLevel.has(lv)) byLevel.set(lv, [])
			byLevel.get(lv)!.push(n)
		})

		// Draw edges from each level's nodes to next level's nodes
		byLevel.forEach((nodes, level) => {
			const nextNodes = byLevel.get(level + 1) ?? []
			nodes.forEach((src) => {
				nextNodes.forEach((dst) => {
					if (nextNodes.length > 1 && nodes.length > 1) return // avoid clutter in wide graphs
					const x1 = src.x + NODE_W / 2,
						y1 = src.y + NODE_H
					const x2 = dst.x + NODE_W / 2,
						y2 = dst.y
					const cy = (y1 + y2) / 2
					edges.push({
						id: `${src.id}-${dst.id}`,
						d: `M${x1},${y1} C${x1},${cy} ${x2},${cy} ${x2},${y2}`,
						color: dst.status === 'running' ? '#3b9eff' : COLORS.edge,
						animated: dst.status === 'running',
					})
				})
			})
		})
		return edges
	})

	// ── Helpers ───────────────────────────────────────────────
	function completedTasks(exec: WorkflowExecution) {
		return exec.tasks?.filter((t) => t.status === 'completed').length ?? 0
	}

	function completePct(exec: WorkflowExecution) {
		const total = exec.tasks?.length ?? 0
		if (!total) return 0
		return Math.round((completedTasks(exec) / total) * 100)
	}

	function statusColor(status: string) {
		return STATUS_COLORS[status as keyof typeof STATUS_COLORS] ?? '#9090a8'
	}

	function elapsed(exec: WorkflowExecution) {
		if (!exec.started_at) return '—'
		const ms =
			(exec.completed_at ? new Date(exec.completed_at) : new Date(now.value)).getTime() -
			new Date(exec.started_at).getTime()
		if (ms < 1000) return `${ms}ms`
		if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
		return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
	}

	function taskDuration(t: TaskExecution) {
		if (!t.started_at) return '—'
		const ms =
			(t.completed_at ? new Date(t.completed_at) : new Date(now.value)).getTime() -
			new Date(t.started_at).getTime()
		if (ms < 1000) return `${ms}ms`
		return `${(ms / 1000).toFixed(1)}s`
	}

	function formatDate(ts: string) {
		return new Date(ts).toLocaleDateString('en', {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
		})
	}

	function taskStats(exec: WorkflowExecution) {
		const counts: Record<string, number> = {}
		exec.tasks?.forEach((t) => {
			counts[t.status] = (counts[t.status] ?? 0) + 1
		})
		return Object.entries(counts).map(([label, count]) => ({
			label,
			count,
			color: statusColor(label),
		}))
	}

	// Reset selectedTaskDefId when exec changes
	watch(
		() => props.selectedId,
		() => {
			selectedTaskDefId.value = null
		},
	)
</script>

<style scoped>
	.executions-view {
		display: flex;
		height: 100%;
		overflow: hidden;
	}

	/* ── List panel ──────────────────────────────────────────── */
	.exec-list-panel {
		width: 320px;
		flex-shrink: 0;
		display: flex;
		flex-direction: column;
		border-right: 1px solid var(--border);
		background: var(--bg2);
	}

	.list-header {
		padding: 12px 14px;
		flex-shrink: 0;
		border-bottom: 1px solid var(--border);
		display: flex;
		align-items: center;
		justify-content: space-between;
	}
	.list-title {
		font-size: 13px;
		font-weight: 600;
	}

	.filter-select {
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text2);
		border-radius: var(--radius-sm);
		padding: 4px 8px;
		font-size: 11px;
		outline: none;
	}

	.list-scroll {
		flex: 1;
		overflow-y: auto;
	}
	.list-scroll::-webkit-scrollbar {
		width: 3px;
	}
	.list-scroll::-webkit-scrollbar-thumb {
		background: var(--border2);
	}

	.list-empty {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 8px;
		padding: 40px 16px;
		color: var(--text3);
		font-size: 13px;
	}
	.list-empty-icon {
		font-size: 28px;
		opacity: 0.3;
	}

	.exec-item {
		padding: 10px 14px;
		cursor: pointer;
		border-bottom: 1px solid var(--border);
		border-left: 2px solid transparent;
		transition: background 0.1s;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.exec-item:hover {
		background: var(--bg3);
	}
	.exec-item.active {
		background: rgba(124, 106, 255, 0.08);
		border-left-color: var(--accent);
	}

	.exec-item-row1 {
		display: flex;
		align-items: center;
		gap: 7px;
	}
	.exec-wf-name {
		font-size: 12px;
		font-weight: 600;
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.exec-elapsed {
		font-size: 10px;
		color: var(--text3);
		font-family: var(--mono);
		flex-shrink: 0;
	}

	.exec-item-row2 {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.mini-progress {
		flex: 1;
		height: 3px;
		background: var(--surface2);
		border-radius: 2px;
		overflow: hidden;
	}
	.mini-progress-fill {
		height: 100%;
		border-radius: 2px;
		transition: width 0.4s;
	}
	.exec-task-frac {
		font-size: 10px;
		color: var(--text3);
		font-family: var(--mono);
		flex-shrink: 0;
	}

	.exec-item-row3 {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}
	.exec-id-mono {
		font-size: 9px;
		color: var(--text3);
		font-family: var(--mono);
	}
	.exec-date {
		font-size: 9px;
		color: var(--text3);
	}

	/* ── Detail panel ────────────────────────────────────────── */
	.exec-detail-panel {
		flex: 1;
		display: flex;
		flex-direction: column;
		overflow: hidden;
		background: var(--bg);
	}

	.detail-empty {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 10px;
		color: var(--text3);
	}
	.detail-empty-icon {
		font-size: 36px;
		opacity: 0.25;
	}
	.detail-empty-text {
		font-size: 13px;
	}

	/* Header */
	.detail-header {
		padding: 12px 16px;
		flex-shrink: 0;
		border-bottom: 1px solid var(--border);
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		background: var(--bg2);
	}
	.detail-header-left {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.detail-wf-name {
		font-size: 14px;
		font-weight: 600;
	}
	.detail-exec-id {
		font-size: 10px;
		color: var(--text3);
		font-family: var(--mono);
		margin-top: 2px;
	}
	.detail-header-right {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.detail-elapsed {
		font-size: 12px;
		color: var(--text2);
		font-family: var(--mono);
	}

	.hdr-btn {
		padding: 4px 12px;
		border-radius: var(--radius-sm);
		font-size: 11px;
		font-weight: 500;
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text2);
	}
	.hdr-btn.retry {
		color: var(--amber);
		border-color: rgba(245, 166, 35, 0.3);
		background: rgba(245, 166, 35, 0.08);
	}
	.hdr-btn.retry:hover {
		background: rgba(245, 166, 35, 0.15);
	}

	/* Stats */
	.stats-row {
		display: flex;
		flex-shrink: 0;
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}
	.stat-cell {
		flex: 1;
		padding: 8px 14px;
		border-right: 1px solid var(--border);
	}
	.stat-cell:last-child {
		border-right: none;
	}
	.stat-val {
		font-size: 18px;
		font-weight: 700;
		font-family: var(--mono);
		line-height: 1;
	}
	.stat-lbl {
		font-size: 9px;
		color: var(--text3);
		text-transform: uppercase;
		letter-spacing: 0.07em;
		margin-top: 2px;
	}

	/* Progress */
	.detail-progress-track {
		height: 3px;
		background: var(--surface2);
		flex-shrink: 0;
	}
	.detail-progress-fill {
		height: 100%;
		transition: width 0.5s ease;
	}

	/* DAG */
	.detail-dag {
		height: 200px;
		flex-shrink: 0;
		border-bottom: 1px solid var(--border);
		background: var(--bg3);
		overflow: hidden;
	}
	.dag-svg {
		overflow: visible;
	}

	/* Task list */
	.task-list {
		flex: 1;
		overflow-y: auto;
		padding: 4px 0;
	}
	.task-list::-webkit-scrollbar {
		width: 4px;
	}
	.task-list::-webkit-scrollbar-thumb {
		background: var(--border2);
	}

	.task-row {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 16px;
		border-bottom: 1px solid var(--border);
		cursor: pointer;
		font-size: 12px;
		transition: background 0.1s;
	}
	.task-row:hover {
		background: var(--bg3);
	}
	.task-row-selected {
		background: rgba(124, 106, 255, 0.06);
	}
	.task-name {
		font-weight: 500;
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.task-type-tag {
		font-size: 9px;
		color: var(--text3);
		font-family: var(--mono);
		background: var(--surface);
		padding: 1px 6px;
		border-radius: 4px;
		flex-shrink: 0;
	}
	.task-worker {
		font-size: 9px;
		color: var(--text3);
		font-family: var(--mono);
		width: 64px;
		text-align: right;
		flex-shrink: 0;
	}
	.task-duration {
		font-size: 10px;
		color: var(--text2);
		font-family: var(--mono);
		width: 44px;
		text-align: right;
		flex-shrink: 0;
	}
	.task-retry {
		font-size: 10px;
		color: var(--amber);
		flex-shrink: 0;
	}

	/* Task detail expand */
	.task-detail {
		padding: 12px 16px;
		background: var(--bg2);
		border-bottom: 1px solid var(--border);
	}
	.task-detail-grid {
		display: grid;
		grid-template-columns: 80px 1fr;
		gap: 4px 12px;
		font-size: 11px;
		margin-bottom: 8px;
	}
	.td-key {
		color: var(--text3);
		font-weight: 500;
	}
	.td-val {
		color: var(--text2);
		font-family: var(--mono);
	}
	.task-error {
		font-size: 11px;
		color: var(--red);
		background: rgba(255, 95, 87, 0.08);
		border: 1px solid rgba(255, 95, 87, 0.2);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		margin-bottom: 8px;
		font-family: var(--mono);
	}
	.task-mini-logs {
		background: #0a0a12;
		border-radius: var(--radius-sm);
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 2px;
		max-height: 130px;
		overflow-y: auto;
	}
	.mini-log {
		display: flex;
		gap: 7px;
		font-size: 10px;
		font-family: var(--mono);
		line-height: 1.6;
	}
	.ml-ts {
		color: #4a5568;
		flex-shrink: 0;
	}
	.ml-level {
		flex-shrink: 0;
		font-weight: 700;
	}
	.mini-log.info .ml-level {
		color: #63b3ed;
	}
	.mini-log.warn .ml-level {
		color: #ecc94b;
	}
	.mini-log.error .ml-level {
		color: #fc8181;
	}
	.ml-msg {
		color: #9fafc0;
	}

	/* Transitions */
	.exec-item-enter-active {
		transition: all 0.2s ease;
	}
	.exec-item-enter-from {
		opacity: 0;
		transform: translateY(-4px);
	}

	.expand-enter-active,
	.expand-leave-active {
		transition: all 0.2s ease;
		overflow: hidden;
	}
	.expand-enter-from,
	.expand-leave-to {
		max-height: 0;
		opacity: 0;
	}
	.expand-enter-to,
	.expand-leave-from {
		max-height: 400px;
		opacity: 1;
	}
</style>
