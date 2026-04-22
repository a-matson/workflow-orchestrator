<template>
	<div class="executions-page">
		<!-- Left: execution list -->
		<div class="exec-list-panel">
			<div class="panel-header">
				<h2 class="panel-title">Runs</h2>
				<select v-model="statusFilter" class="filter-select">
					<option value="">All</option>
					<option value="running">Running</option>
					<option value="completed">Completed</option>
					<option value="failed">Failed</option>
					<option value="pending">Pending</option>
				</select>
			</div>

			<div v-if="store.loading" class="list-loading">Loading…</div>

			<div class="exec-list-scroll">
				<div
					v-for="exec in filteredExecs"
					:key="exec.id"
					class="exec-item"
					:class="{ active: selectedId === exec.id }"
					@click="selectExec(exec.id)"
				>
					<div class="exec-item-row1">
						<span :class="['badge', exec.status]">{{ exec.status }}</span>
						<span class="exec-wf-name">{{ exec.workflow_name }}</span>
					</div>
					<div class="exec-item-row2">
						<div class="exec-progress-bar">
							<div
								class="exec-progress-fill"
								:style="{ width: completePct(exec) + '%', background: statusColor(exec.status) }"
							></div>
						</div>
						<span class="exec-task-frac"
							>{{ completedTasks(exec) }}/{{ exec.tasks?.length ?? 0 }}</span
						>
					</div>
					<div class="exec-item-row3">
						<span class="exec-id-mono">{{ exec.id.slice(0, 12) }}…</span>
						<span class="exec-elapsed">{{ elapsedStr(exec.started_at, exec.completed_at) }}</span>
					</div>
				</div>
				<div v-if="!filteredExecs.length && !store.loading" class="list-empty">
					No executions yet — run a workflow from the Builder.
				</div>
			</div>
		</div>

		<!-- Right: execution detail -->
		<div class="exec-detail-panel">
			<template v-if="selectedExec">
				<!-- Header -->
				<div class="detail-header">
					<div class="detail-header-left">
						<span :class="['badge', selectedExec.status]">{{ selectedExec.status }}</span>
						<div>
							<div class="detail-wf-name">{{ selectedExec.workflow_name }}</div>
							<div class="detail-exec-id">{{ selectedExec.id }}</div>
						</div>
					</div>
					<div class="detail-header-right">
						<button
							v-if="selectedExec.status === 'failed' || selectedExec.status === 'completed'"
							class="btn-action retry"
							@click="retryExec(selectedExec.id)"
						>
							↺ Retry
						</button>
						<button
							v-if="selectedExec.status === 'running'"
							class="btn-action cancel"
							@click="cancelExec(selectedExec.id)"
						>
							◼ Cancel
						</button>
						<button class="btn-action" @click="goToLogs(selectedExec.id)">≡ Logs</button>
						<span class="detail-elapsed">{{
							elapsedStr(selectedExec.started_at, selectedExec.completed_at)
						}}</span>
					</div>
				</div>

				<!-- Stats row -->
				<div class="stats-row">
					<div v-for="s in taskStats(selectedExec)" :key="s.label" class="stat-cell">
						<div class="stat-val" :style="{ color: s.color }">{{ s.count }}</div>
						<div class="stat-lbl">{{ s.label }}</div>
					</div>
					<div class="stat-cell">
						<div class="stat-val">{{ completePct(selectedExec) }}%</div>
						<div class="stat-lbl">progress</div>
					</div>
				</div>

				<!-- Progress bar -->
				<div class="progress-track">
					<div
						class="progress-fill"
						:style="{
							width: completePct(selectedExec) + '%',
							background: statusColor(selectedExec.status),
						}"
					></div>
				</div>

				<!-- Task list -->
				<div class="task-list">
					<div
						v-for="task in selectedExec.tasks"
						:key="task.id"
						class="task-row"
						:class="{ 'task-row--selected': expandedTaskId === task.id }"
						@click="expandedTaskId = expandedTaskId === task.id ? null : task.id"
					>
						<span :class="['badge', task.status]">{{ task.status }}</span>
						<span class="task-name">{{ task.task_name }}</span>
						<span class="task-type">{{ task.task_type }}</span>
						<span class="task-worker">{{ task.worker_id?.slice(-8) ?? '' }}</span>
						<span class="task-dur">{{ taskDuration(task) }}</span>
						<span v-if="task.retry_count > 0" class="task-retry">↺{{ task.retry_count }}</span>
					</div>

					<!-- Expanded task detail -->
					<Transition name="expand">
						<div v-if="expandedTask" class="task-detail">
							<div v-if="expandedTask.error" class="task-error">{{ expandedTask.error }}</div>
							<div class="task-detail-grid">
								<span class="td-key">Worker</span>
								<span class="td-val">{{ expandedTask.worker_id ?? '—' }}</span>
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
									>{{ expandedTask.retry_count }}/{{ expandedTask.max_retries }}</span
								>
							</div>
							<!-- Artifacts produced by this task -->
							<template v-if="expandedTask.artifacts_out?.length">
								<div class="td-key" style="margin-top: 6px">Artifacts out</div>
								<div class="artifact-list">
									<a
										v-for="art in expandedTask.artifacts_out"
										:key="art.minio_key"
										class="artifact-chip"
										:href="artifactDownloadUrl(art.minio_key)"
										target="_blank"
										:title="art.minio_key"
									>
										<span class="artifact-icon">⬇</span>
										{{ art.path }}
										<span class="artifact-size">{{ fmtBytes(art.size) }}</span>
									</a>
								</div>
							</template>

							<!-- Streaming terminal output -->
							<div class="term-section">
								<button class="term-toggle" @click.stop="termOpen = !termOpen">
									<span class="term-toggle-icon">{{ termOpen ? '▾' : '▸' }}</span>
									<span class="term-label">Terminal output</span>
									<span class="term-count">{{ expandedTask.logs?.length ?? 0 }} lines</span>
									<span v-if="expandedTask.status === 'running'" class="term-live">● LIVE</span>
								</button>
								<Transition name="term-slide">
									<div v-if="termOpen" class="term-body">
										<div ref="termEl" class="term-output">
											<div v-if="!expandedTask.logs?.length" class="term-empty">
												{{
													expandedTask.status === 'running'
														? 'Waiting for output…'
														: 'No output recorded'
												}}
											</div>
											<div
												v-for="(log, i) in expandedTask.logs ?? []"
												:key="i"
												class="term-line"
												:class="log.level"
											>
												<span class="tl-ts">{{ fmtLogTs(log.timestamp) }}</span>
												<span class="tl-lvl" :class="log.level">{{
													(log.level ?? 'info').toUpperCase().slice(0, 1)
												}}</span>
												<span class="tl-msg">{{ log.message }}</span>
												<span v-if="hasFields(log.fields)" class="tl-fields">{{
													fmtFields(log.fields)
												}}</span>
											</div>
										</div>
										<div class="term-footer">
											<label class="term-autoscroll">
												<input v-model="termAutoScroll" type="checkbox" @click.stop /> auto-scroll
											</label>
											<button class="term-copy-btn" @click.stop="copyTermLogs">copy</button>
										</div>
									</div>
								</Transition>
							</div>
						</div>
					</Transition>
				</div>
			</template>

			<div v-else class="empty-detail">
				<div class="empty-icon">⬡</div>
				<div class="empty-text">Select a run to see details</div>
			</div>
		</div>
	</div>
</template>

<script setup lang="ts">
	import { ref, computed, inject, onMounted, watch, nextTick } from 'vue'
	import { useRoute, useRouter } from 'vue-router'
	import { useWorkflowStore } from '../stores/workflow'
	import { useWebSocketStore } from '../stores/websocket'
	import { STATUS_COLORS } from '../types'
	import type { WorkflowExecution, TaskExecution } from '../types'

	const store = useWorkflowStore()
	const wsStore = useWebSocketStore()
	const route = useRoute()
	const router = useRouter()
	const showToast = inject<(msg: string, type?: 'success' | 'error' | 'info') => void>('showToast')

	const statusFilter = ref('')
	const selectedId = ref<string | null>(null)
	const expandedTaskId = ref<string | null>(null)

	const filteredExecs = computed(() =>
		store.executions.filter((e) => !statusFilter.value || e.status === statusFilter.value),
	)

	const selectedExec = computed(
		() => store.executions.find((e) => e.id === selectedId.value) ?? null,
	)

	const expandedTask = computed(
		() => selectedExec.value?.tasks?.find((t) => t.id === expandedTaskId.value) ?? null,
	)

	onMounted(async () => {
		await store.fetchExecutions()
		// Support deep link: /executions/:execId
		const routeExecId = route.params.execId as string | undefined
		if (routeExecId) {
			await store.fetchExecution(routeExecId)
			selectedId.value = routeExecId
			wsStore.subscribe(routeExecId)
		}
	})

	async function selectExec(id: string) {
		selectedId.value = id
		expandedTaskId.value = null
		await store.fetchExecution(id)
		wsStore.subscribe(id)
		router.replace({ name: 'execution-detail', params: { execId: id } })
	}

	async function retryExec(id: string) {
		try {
			const newExec = await store.retryExecution(id)
			wsStore.subscribe(newExec.id)
			selectedId.value = newExec.id
			showToast?.('↺ Re-queued', 'info')
		} catch {
			showToast?.('Retry failed', 'error')
		}
	}

	async function cancelExec(id: string) {
		try {
			await store.cancelExecution(id)
			showToast?.('◼ Cancellation requested', 'info')
		} catch {
			showToast?.('Cancel failed', 'error')
		}
	}

	function goToLogs(execId: string) {
		router.push({ name: 'logs-execution', params: { execId } })
	}

	// ── Helpers ────────────────────────────────────────────────────
	function completedTasks(exec: WorkflowExecution) {
		return exec.tasks?.filter((t) => t.status === 'completed').length ?? 0
	}

	function completePct(exec: WorkflowExecution) {
		const total = exec.tasks?.length ?? 0
		return total ? Math.round((completedTasks(exec) / total) * 100) : 0
	}

	function statusColor(status: string) {
		return STATUS_COLORS[status as keyof typeof STATUS_COLORS] ?? '#9090a8'
	}

	function elapsedStr(startAt?: string, endAt?: string) {
		if (!startAt) return '—'
		const ms = (endAt ? new Date(endAt) : new Date()).getTime() - new Date(startAt).getTime()
		if (ms < 1000) return `${ms}ms`
		if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
		return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
	}

	function taskDuration(task: TaskExecution) {
		if (!task.started_at) return '—'
		const ms =
			(task.completed_at ? new Date(task.completed_at) : new Date()).getTime() -
			new Date(task.started_at).getTime()
		if (ms < 1000) return `${ms}ms`
		return `${(ms / 1000).toFixed(1)}s`
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

	// Keep selectedExec fresh when WS events update it
	watch(
		() => store.executions.find((e) => e.id === selectedId.value),
		(updated) => {
			if (updated) store.selectedExecution = updated
		},
	)

	function artifactDownloadUrl(minioKey: string): string {
		return `/api/artifacts/url?key=${encodeURIComponent(minioKey)}&expires=60`
	}

	function fmtBytes(bytes: number): string {
		if (!bytes) return ''
		if (bytes < 1024) return `${bytes}B`
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}KB`
		return `${(bytes / (1024 * 1024)).toFixed(1)}MB`
	}

	// ── Terminal state ──────────────────────────────────────────────
	const termOpen = ref(true)
	const termAutoScroll = ref(true)
	const termEl = ref<HTMLElement | null>(null)

	// Auto-scroll terminal when new log lines arrive
	watch(
		() => expandedTask.value?.logs?.length,
		async () => {
			if (!termAutoScroll.value || !termOpen.value) return
			await nextTick()
			if (termEl.value) termEl.value.scrollTop = termEl.value.scrollHeight
		},
	)

	// Reset terminal open state when switching tasks
	watch(expandedTaskId, () => {
		termOpen.value = true
		termAutoScroll.value = true
	})

	function fmtLogTs(ts: string): string {
		const d = new Date(ts)
		return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}.${String(d.getMilliseconds()).padStart(3, '0')}`
	}

	function fmtFields(fields: Record<string, unknown> | undefined): string {
		if (!fields) return ''
		const parts = Object.entries(fields)
			.filter(([k]) => k !== 'output' && k !== 'stdout')
			.map(([k, v]) => `${k}=${JSON.stringify(v)}`)
		return parts.join(' ')
	}

	function hasFields(fields: Record<string, unknown> | undefined): boolean {
		if (!fields) return false
		const filtered = Object.keys(fields).filter((k) => k !== 'output' && k !== 'stdout')
		return filtered.length > 0
	}

	function copyTermLogs() {
		const logs = expandedTask.value?.logs ?? []
		const text = logs
			.map(
				(l) =>
					`[${fmtLogTs(l.timestamp)}] ${(l.level ?? '').toUpperCase().padEnd(5)} ${l.message}${hasFields(l.fields) ? ' ' + fmtFields(l.fields) : ''}`,
			)
			.join('\n')
		navigator.clipboard.writeText(text)
	}
</script>

<style scoped>
	.executions-page {
		display: flex;
		height: 100%;
		overflow: hidden;
	}

	/* List panel */
	.exec-list-panel {
		width: 300px;
		flex-shrink: 0;
		background: var(--bg2);
		border-right: 1px solid var(--border);
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}
	.panel-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 10px 14px;
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
	}
	.panel-title {
		font-size: 13px;
		font-weight: 600;
	}
	.filter-select {
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text2);
		border-radius: var(--r-sm);
		padding: 4px 8px;
		font-size: 11px;
		outline: none;
	}
	.list-loading,
	.list-empty {
		padding: 16px 14px;
		font-size: 12px;
		color: var(--text3);
	}
	.exec-list-scroll {
		flex: 1;
		overflow-y: auto;
	}

	.exec-item {
		padding: 10px 14px;
		cursor: pointer;
		border-bottom: 1px solid var(--border);
		border-left: 2px solid transparent;
		transition: background 0.1s;
		display: flex;
		flex-direction: column;
		gap: 5px;
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
	.exec-item-row2 {
		display: flex;
		align-items: center;
		gap: 7px;
	}
	.exec-progress-bar {
		flex: 1;
		height: 3px;
		background: var(--surface2);
		border-radius: 2px;
		overflow: hidden;
	}
	.exec-progress-fill {
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
		justify-content: space-between;
	}
	.exec-id-mono {
		font-size: 9px;
		color: var(--text3);
		font-family: var(--mono);
	}
	.exec-elapsed {
		font-size: 9px;
		color: var(--text3);
		font-family: var(--mono);
	}

	/* Detail panel */
	.exec-detail-panel {
		flex: 1;
		display: flex;
		flex-direction: column;
		overflow: hidden;
		background: var(--bg);
	}
	.empty-detail {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 10px;
		color: var(--text3);
	}
	.empty-icon {
		font-size: 36px;
		opacity: 0.2;
	}
	.empty-text {
		font-size: 13px;
	}

	.detail-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border);
		background: var(--bg2);
		flex-shrink: 0;
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
	}
	.detail-header-right {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.detail-elapsed {
		font-size: 12px;
		color: var(--text2);
		font-family: var(--mono);
	}
	.btn-action {
		padding: 4px 11px;
		border-radius: var(--r-sm);
		font-size: 11px;
		font-weight: 500;
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text2);
	}
	.btn-action:hover {
		color: var(--text);
		background: var(--surface2);
	}
	.btn-action.retry {
		color: var(--amber);
		border-color: rgba(245, 166, 35, 0.3);
		background: rgba(245, 166, 35, 0.08);
	}
	.btn-action.cancel {
		color: var(--red);
		border-color: rgba(255, 95, 87, 0.3);
		background: rgba(255, 95, 87, 0.08);
	}

	.stats-row {
		display: flex;
		border-bottom: 1px solid var(--border);
		background: var(--surface);
		flex-shrink: 0;
	}
	.stat-cell {
		flex: 1;
		padding: 8px 12px;
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

	.progress-track {
		height: 3px;
		background: var(--surface2);
		flex-shrink: 0;
	}
	.progress-fill {
		height: 100%;
		transition: width 0.5s ease;
	}

	.task-list {
		flex: 1;
		overflow-y: auto;
	}
	.task-row {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 16px;
		border-bottom: 1px solid var(--border);
		font-size: 12px;
		cursor: pointer;
		transition: background 0.1s;
	}
	.task-row:hover {
		background: var(--bg2);
	}
	.task-row--selected {
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
	.task-type {
		font-size: 9px;
		color: var(--text3);
		font-family: var(--mono);
		background: var(--surface);
		padding: 1px 5px;
		border-radius: 3px;
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
	.task-dur {
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

	.task-detail {
		padding: 12px 16px;
		background: var(--bg2);
		border-bottom: 1px solid var(--border);
	}
	.task-error {
		font-size: 11px;
		color: var(--red);
		background: rgba(255, 95, 87, 0.08);
		border: 1px solid rgba(255, 95, 87, 0.2);
		border-radius: var(--r-sm);
		padding: 6px 10px;
		font-family: var(--mono);
		margin-bottom: 8px;
	}
	.task-detail-grid {
		display: grid;
		grid-template-columns: 70px 1fr;
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
	.task-mini-logs {
		background: #090910;
		border-radius: var(--r-sm);
		padding: 6px;
		max-height: 120px;
		overflow-y: auto;
	}
	.mini-log {
		display: flex;
		gap: 6px;
		font-size: 10px;
		font-family: var(--mono);
		line-height: 1.7;
	}
	.ml-ts {
		color: #4a5568;
		flex-shrink: 0;
	}
	.ml-lvl {
		width: 34px;
		font-size: 9px;
		font-weight: 700;
		flex-shrink: 0;
	}
	.mini-log.info .ml-lvl {
		color: #63b3ed;
	}
	.mini-log.warn .ml-lvl {
		color: #ecc94b;
	}
	.mini-log.error .ml-lvl {
		color: #fc8181;
	}
	.ml-msg {
		color: #9fafc0;
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

	.artifact-list {
		display: flex;
		flex-wrap: wrap;
		gap: 5px;
		margin-top: 4px;
	}
	.artifact-chip {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 3px 9px;
		border-radius: 4px;
		font-size: 10px;
		font-family: var(--mono);
		background: rgba(34, 211, 160, 0.08);
		color: var(--green);
		border: 1px solid rgba(34, 211, 160, 0.2);
		text-decoration: none;
	}
	.artifact-chip:hover {
		background: rgba(34, 211, 160, 0.18);
	}
	.artifact-icon {
		font-size: 9px;
	}
	.artifact-size {
		color: var(--text3);
		font-size: 9px;
	}

	/* ── Streaming terminal ─────────────────────────────────── */
	.term-section {
		margin-top: 10px;
		border: 1px solid rgba(255, 255, 255, 0.06);
		border-radius: 6px;
		overflow: hidden;
	}

	.term-toggle {
		display: flex;
		align-items: center;
		gap: 7px;
		width: 100%;
		padding: 6px 10px;
		background: rgba(255, 255, 255, 0.03);
		border: none;
		cursor: pointer;
		font-size: 11px;
		color: var(--text2);
		text-align: left;
	}
	.term-toggle:hover {
		background: rgba(255, 255, 255, 0.06);
	}
	.term-toggle-icon {
		font-size: 10px;
		color: var(--text3);
		width: 10px;
	}
	.term-label {
		font-weight: 600;
		font-size: 11px;
		color: var(--text2);
	}
	.term-count {
		font-size: 10px;
		color: var(--text3);
		font-family: var(--mono);
		margin-left: auto;
	}
	.term-live {
		font-size: 9px;
		font-weight: 700;
		color: var(--green);
		background: rgba(34, 211, 160, 0.1);
		border: 1px solid rgba(34, 211, 160, 0.25);
		border-radius: 3px;
		padding: 1px 5px;
		letter-spacing: 0.04em;
		animation: pulse-live 1.5s ease-in-out infinite;
	}
	@keyframes pulse-live {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.5;
		}
	}

	.term-body {
		background: #07070e;
		border-top: 1px solid rgba(255, 255, 255, 0.05);
	}

	.term-output {
		max-height: 320px;
		overflow-y: auto;
		padding: 6px 0;
		font-family: var(--mono);
		font-size: 11px;
	}
	.term-empty {
		padding: 14px 12px;
		color: #3a3a55;
		font-style: italic;
	}

	.term-line {
		display: flex;
		align-items: baseline;
		gap: 6px;
		padding: 1px 12px;
		line-height: 1.75;
	}
	.term-line:hover {
		background: rgba(255, 255, 255, 0.02);
	}
	.tl-ts {
		color: #2a2a42;
		font-size: 10px;
		flex-shrink: 0;
		min-width: 86px;
	}
	.tl-lvl {
		width: 14px;
		text-align: center;
		font-size: 9px;
		font-weight: 800;
		flex-shrink: 0;
		border-radius: 2px;
	}
	.tl-lvl.info {
		color: #4a7fa8;
	}
	.tl-lvl.warn {
		color: #b8860b;
	}
	.tl-lvl.error {
		color: #a83232;
	}
	.tl-lvl.debug {
		color: #555;
	}
	.tl-msg {
		color: #a8b5c8;
		flex: 1;
		word-break: break-word;
		white-space: pre-wrap;
	}
	.term-line.warn .tl-msg {
		color: #c8a835;
	}
	.term-line.error .tl-msg {
		color: #d46060;
	}
	.tl-fields {
		font-size: 9.5px;
		color: #2a2a42;
		word-break: break-all;
	}

	.term-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 4px 10px;
		border-top: 1px solid rgba(255, 255, 255, 0.04);
		background: rgba(0, 0, 0, 0.2);
	}
	.term-autoscroll {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 10px;
		color: var(--text3);
		cursor: pointer;
	}
	.term-autoscroll input {
		cursor: pointer;
	}
	.term-copy-btn {
		font-size: 10px;
		padding: 2px 8px;
		background: var(--surface);
		border: 1px solid var(--border2);
		border-radius: 3px;
		color: var(--text3);
	}
	.term-copy-btn:hover {
		color: var(--text);
	}

	.term-slide-enter-active,
	.term-slide-leave-active {
		transition: all 0.18s ease;
		overflow: hidden;
	}
	.term-slide-enter-from,
	.term-slide-leave-to {
		max-height: 0;
		opacity: 0;
	}
	.term-slide-enter-to,
	.term-slide-leave-from {
		max-height: 380px;
		opacity: 1;
	}
</style>
