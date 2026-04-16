<template>
	<div class="logs-page">
		<!-- Left: execution picker -->
		<div class="logs-sidebar">
			<div class="sidebar-header">
				<span class="sidebar-title">Executions</span>
			</div>
			<div class="exec-picker">
				<div
					v-for="exec in store.executions.slice(0, 40)"
					:key="exec.id"
					class="picker-item"
					:class="{ active: selectedId === exec.id }"
					@click="selectExec(exec.id)"
				>
					<span :class="['badge', exec.status]">{{ exec.status }}</span>
					<span class="picker-name">{{ exec.workflow_name }}</span>
					<span class="picker-elapsed">{{ elapsed(exec.started_at, exec.completed_at) }}</span>
				</div>
				<div v-if="!store.executions.length" class="picker-empty">No executions yet</div>
			</div>
		</div>

		<!-- Right: log terminal -->
		<div class="log-panel">
			<template v-if="selectedExec">
				<!-- Toolbar -->
				<div class="log-toolbar">
					<div class="toolbar-left">
						<input v-model="search" class="log-search" placeholder="Search logs…" />
						<button
							v-for="lvl in levels"
							:key="lvl"
							class="level-btn"
							:class="{ active: activeLevels.has(lvl) }"
							@click="toggleLevel(lvl)"
						>
							{{ lvl }}
						</button>
						<select v-model="taskFilter" class="task-select">
							<option value="">All tasks</option>
							<option v-for="t in selectedExec.tasks" :key="t.id" :value="t.id">
								{{ t.task_name }}
							</option>
						</select>
					</div>
					<div class="toolbar-right">
						<span class="log-count">{{ displayLogs.length }} entries</span>
						<label class="autoscroll-label">
							<input v-model="autoScroll" type="checkbox" />auto-scroll
						</label>
						<button class="log-btn" @click="copyLogs">Copy</button>
						<button class="log-btn" @click="clearView">Clear</button>
					</div>
				</div>

				<!-- Log output -->
				<div ref="logEl" class="log-output" @scroll="onScroll">
					<div v-if="!displayLogs.length" class="log-empty">
						{{
							selectedExec.status === 'running' ? 'Waiting for logs…' : 'No logs match your filter'
						}}
					</div>
					<div v-for="(entry, i) in displayLogs" :key="i" class="log-line" :class="entry.level">
						<span class="ll-ts">{{ fmtTs(entry.timestamp) }}</span>
						<span class="ll-lvl" :class="entry.level">{{ entry.level?.toUpperCase() }}</span>
						<span class="ll-task">{{ entry._taskName }}</span>
						<span class="ll-msg">
							<template v-for="(seg, idx) in getHighlightSegments(entry.message)" :key="idx">
								<mark
									v-if="seg.match"
									style="background: rgba(124, 106, 255, 0.3); color: inherit; border-radius: 2px"
								>
									{{ seg.text }}
								</mark>
								<template v-else>{{ seg.text }}</template>
							</template>
						</span>
						<span v-if="hasFields(entry.fields)" class="ll-fields">{{
							JSON.stringify(entry.fields)
						}}</span>
					</div>
				</div>
			</template>

			<div v-else class="log-empty-state">
				<div class="lei-icon">≡</div>
				<div class="lei-text">Select an execution to view its logs</div>
			</div>
		</div>
	</div>
</template>

<script setup lang="ts">
	import { ref, computed, watch, nextTick, onMounted } from 'vue'
	import { useRoute } from 'vue-router'
	import { useWorkflowStore } from '../stores/workflow'
	import { useWebSocketStore } from '../stores/websocket'
	import type { LogEntry } from '../types'

	interface EnrichedLog extends LogEntry {
		_taskId: string
		_taskName: string
	}

	const store = useWorkflowStore()
	const wsStore = useWebSocketStore()
	const route = useRoute()

	const selectedId = ref<string | null>(null)
	const search = ref('')
	const taskFilter = ref('')
	const autoScroll = ref(true)
	const cleared = ref(false)
	const logEl = ref<HTMLElement | null>(null)

	const levels = ['info', 'warn', 'error', 'debug'] as const
	const activeLevels = ref(new Set<string>(['info', 'warn', 'error', 'debug']))

	const selectedExec = computed(
		() => store.executions.find((e) => e.id === selectedId.value) ?? null,
	)

	const allLogs = computed((): EnrichedLog[] => {
		if (cleared.value || !selectedExec.value) return []
		const logs: EnrichedLog[] = []
		for (const task of selectedExec.value.tasks ?? []) {
			for (const log of task.logs ?? []) {
				logs.push({ ...log, _taskId: task.id, _taskName: task.task_name })
			}
		}
		return logs.sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
	})

	const displayLogs = computed(() => {
		const q = search.value.toLowerCase()
		return allLogs.value.filter((l) => {
			if (!activeLevels.value.has(l.level ?? 'info')) return false
			if (taskFilter.value && l._taskId !== taskFilter.value) return false
			if (q) return l.message?.toLowerCase().includes(q) || l._taskName?.toLowerCase().includes(q)
			return true
		})
	})

	watch(displayLogs, async () => {
		if (!autoScroll.value) return
		await nextTick()
		if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
	})

	onMounted(async () => {
		await store.fetchExecutions()
		const execId = route.params.execId as string | undefined
		if (execId) {
			await store.fetchExecution(execId)
			selectedId.value = execId
		}
	})

	async function selectExec(id: string) {
		selectedId.value = id
		cleared.value = false
		taskFilter.value = ''
		await store.fetchExecution(id)
		wsStore.subscribe(id)
	}

	function toggleLevel(lvl: string) {
		if (activeLevels.value.has(lvl)) activeLevels.value.delete(lvl)
		else activeLevels.value.add(lvl)
	}

	function onScroll() {
		if (!logEl.value) return
		const el = logEl.value
		autoScroll.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
	}

	function clearView() {
		cleared.value = true
	}

	function copyLogs() {
		const text = displayLogs.value
			.map(
				(l) =>
					`[${fmtTs(l.timestamp)}] ${(l.level ?? '').toUpperCase().padEnd(5)} [${l._taskName}] ${l.message}`,
			)
			.join('\n')
		navigator.clipboard.writeText(text)
	}

	function getHighlightSegments(msg: string): Array<{ text: string; match: boolean }> {
		if (!search.value || !msg) return [{ text: msg, match: false }]

		const esc = search.value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
		const regex = new RegExp(`(${esc})`, 'gi')
		const parts = msg.split(regex)

		return parts
			.map((part) => ({
				text: part,
				// If it matches the search query (case-insensitive), mark it
				match: part.toLowerCase() === search.value.toLowerCase(),
			}))
			.filter((p) => p.text.length > 0)
	}

	function fmtTs(ts: string): string {
		const d = new Date(ts)
		return `${d.toLocaleTimeString('en', { hour12: false })}.${String(d.getMilliseconds()).padStart(3, '0')}`
	}

	function hasFields(fields: Record<string, unknown> | undefined): boolean {
		return !!fields && Object.keys(fields).length > 0
	}

	function elapsed(start?: string, end?: string) {
		if (!start) return '—'
		const ms = (end ? new Date(end) : new Date()).getTime() - new Date(start).getTime()
		if (ms < 1000) return `${ms}ms`
		if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
		return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
	}
</script>

<style scoped>
	.logs-page {
		display: flex;
		height: 100%;
		overflow: hidden;
		background: var(--bg);
	}

	.logs-sidebar {
		width: 220px;
		flex-shrink: 0;
		background: var(--bg2);
		border-right: 1px solid var(--border);
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}
	.sidebar-header {
		padding: 10px 14px;
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
	}
	.sidebar-title {
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--text3);
	}
	.exec-picker {
		flex: 1;
		overflow-y: auto;
	}
	.picker-item {
		padding: 8px 14px;
		cursor: pointer;
		border-bottom: 1px solid var(--border);
		border-left: 2px solid transparent;
		display: flex;
		flex-direction: column;
		gap: 3px;
		transition: background 0.1s;
	}
	.picker-item:hover {
		background: var(--bg3);
	}
	.picker-item.active {
		background: rgba(124, 106, 255, 0.08);
		border-left-color: var(--accent);
	}
	.picker-name {
		font-size: 11px;
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.picker-elapsed {
		font-size: 10px;
		color: var(--text3);
		font-family: var(--mono);
	}
	.picker-empty {
		padding: 16px 14px;
		font-size: 11px;
		color: var(--text3);
	}

	.log-panel {
		flex: 1;
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}

	.log-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 7px 12px;
		background: var(--bg2);
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
		gap: 8px;
		flex-wrap: wrap;
	}
	.toolbar-left,
	.toolbar-right {
		display: flex;
		align-items: center;
		gap: 7px;
	}
	.log-search {
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text);
		border-radius: var(--r-sm);
		padding: 4px 9px;
		font-size: 12px;
		font-family: var(--mono);
		outline: none;
		width: 180px;
	}
	.log-search:focus {
		border-color: var(--accent);
	}
	.level-btn {
		padding: 3px 9px;
		background: var(--surface);
		border: 1px solid var(--border2);
		border-radius: 4px;
		font-size: 10px;
		font-weight: 600;
		color: var(--text3);
		text-transform: uppercase;
	}
	.level-btn.active {
		border-color: var(--accent);
		background: rgba(124, 106, 255, 0.12);
		color: var(--accent);
	}
	.task-select {
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text2);
		border-radius: var(--r-sm);
		padding: 4px 8px;
		font-size: 11px;
		outline: none;
	}
	.log-count {
		font-size: 11px;
		color: var(--text3);
		font-family: var(--mono);
	}
	.autoscroll-label {
		font-size: 11px;
		color: var(--text3);
		display: flex;
		align-items: center;
		gap: 4px;
		cursor: pointer;
	}
	.log-btn {
		padding: 3px 9px;
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text3);
		border-radius: var(--r-sm);
		font-size: 11px;
	}
	.log-btn:hover {
		color: var(--text);
		border-color: var(--border2);
	}

	.log-output {
		flex: 1;
		overflow-y: auto;
		background: #09090f;
		padding: 4px 0;
	}
	.log-empty {
		padding: 24px;
		text-align: center;
		color: var(--text3);
		font-size: 12px;
	}
	.log-line {
		display: flex;
		align-items: baseline;
		gap: 8px;
		padding: 1.5px 14px;
		font-family: var(--mono);
		font-size: 11px;
		line-height: 1.8;
	}
	.log-line:hover {
		background: rgba(255, 255, 255, 0.025);
	}
	.ll-ts {
		color: #3a3a55;
		flex-shrink: 0;
		font-size: 10px;
	}
	.ll-lvl {
		width: 38px;
		text-align: center;
		font-size: 9px;
		font-weight: 700;
		border-radius: 3px;
		padding: 0 2px;
		flex-shrink: 0;
	}
	.ll-lvl.info {
		background: rgba(59, 158, 255, 0.1);
		color: #63b3ed;
	}
	.ll-lvl.warn {
		background: rgba(245, 166, 35, 0.1);
		color: #ecc94b;
	}
	.ll-lvl.error {
		background: rgba(255, 95, 87, 0.1);
		color: #fc8181;
	}
	.ll-lvl.debug {
		background: rgba(110, 118, 129, 0.1);
		color: #8b949e;
	}
	.ll-task {
		font-size: 10px;
		color: #7c6aff;
		background: rgba(124, 106, 255, 0.08);
		padding: 0 5px;
		border-radius: 3px;
		flex-shrink: 0;
		max-width: 120px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.ll-msg {
		color: #cbd5e0;
		flex: 1;
		word-break: break-word;
	}
	.log-line.warn .ll-msg {
		color: #ecc94b;
	}
	.log-line.error .ll-msg {
		color: #fc8181;
	}
	.ll-fields {
		font-size: 9.5px;
		color: #3a3a55;
		word-break: break-all;
	}

	.log-empty-state {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 10px;
		color: var(--text3);
	}
	.lei-icon {
		font-size: 40px;
		opacity: 0.18;
	}
	.lei-text {
		font-size: 13px;
	}
</style>
