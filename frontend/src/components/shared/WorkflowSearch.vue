<template>
	<div class="ws-root" :class="{ open: isOpen }">
		<!-- Trigger button -->
		<button class="ws-trigger" title="Search (Ctrl+K)" @click="open">
			<span class="ws-icon">⌕</span>
			<span class="ws-hint">Search</span>
			<kbd class="ws-kbd">⌘K</kbd>
		</button>

		<!-- Modal -->
		<Transition name="modal">
			<div v-if="isOpen" class="ws-overlay" @click.self="close">
				<div class="ws-modal">
					<div class="ws-input-wrap">
						<span class="ws-search-icon">⌕</span>
						<input
							ref="inputEl"
							v-model="query"
							class="ws-input"
							placeholder="Search workflows, executions, tasks…"
							@keydown.escape="close"
							@keydown.down.prevent="moveSelection(1)"
							@keydown.up.prevent="moveSelection(-1)"
							@keydown.enter.prevent="selectCurrent"
						/>
						<button v-if="query" class="ws-clear" @click="query = ''">✕</button>
					</div>

					<div v-if="results.length" class="ws-results">
						<div
							v-for="(result, i) in results"
							:key="result.id"
							class="ws-result"
							:class="{ selected: i === selectedIndex }"
							@click="select(result)"
							@mouseenter="selectedIndex = i"
						>
							<span class="ws-result-icon">{{ result.icon }}</span>
							<div class="ws-result-body">
								<div class="ws-result-title" v-html="highlight(result.title)"></div>
								<div class="ws-result-sub">
									{{ result.subtitle }}
								</div>
							</div>
							<span class="ws-result-type">{{ result.type }}</span>
						</div>
					</div>

					<div v-else-if="query.length > 1" class="ws-empty">
						No results for "<em>{{ query }}</em
						>"
					</div>

					<div class="ws-footer">
						<span><kbd>↑↓</kbd> navigate</span>
						<span><kbd>↵</kbd> select</span>
						<span><kbd>Esc</kbd> close</span>
					</div>
				</div>
			</div>
		</Transition>
	</div>
</template>

<script setup lang="ts">
	import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
	import { useWorkflowStore } from '../../stores/workflow'

	interface SearchResult {
		id: string
		type: 'workflow' | 'execution' | 'task'
		icon: string
		title: string
		subtitle: string
		action: () => void
	}

	const emit = defineEmits<{
		(e: 'navigate-execution', id: string): void
		(e: 'navigate-workflow', id: string): void
	}>()

	const store = useWorkflowStore()
	const isOpen = ref(false)
	const query = ref('')
	const selectedIndex = ref(0)
	const inputEl = ref<HTMLInputElement | null>(null)

	const results = computed<SearchResult[]>(() => {
		const q = query.value.toLowerCase().trim()
		if (q.length < 1) return []

		const out: SearchResult[] = []

		// Workflow definitions
		for (const wf of store.definitions) {
			if (wf.name.toLowerCase().includes(q) || wf.id.includes(q)) {
				out.push({
					id: wf.id,
					type: 'workflow',
					icon: '◈',
					title: wf.name,
					subtitle: `${wf.tasks.length} tasks · v${wf.version}`,
					action: () => {
						emit('navigate-workflow', wf.id)
						close()
					},
				})
			}
		}

		// Executions
		for (const exec of store.executions) {
			if (
				exec.workflow_name.toLowerCase().includes(q) ||
				exec.id.toLowerCase().includes(q) ||
				exec.status.includes(q)
			) {
				out.push({
					id: exec.id,
					type: 'execution',
					icon: exec.status === 'completed' ? '✓' : exec.status === 'failed' ? '✗' : '◌',
					title: exec.workflow_name,
					subtitle: `${exec.id.slice(0, 12)}… · ${exec.status}`,
					action: () => {
						emit('navigate-execution', exec.id)
						close()
					},
				})
			}

			// Tasks within executions
			for (const task of exec.tasks ?? []) {
				if (task.task_name.toLowerCase().includes(q) || task.worker_id?.includes(q)) {
					out.push({
						id: task.id,
						type: 'task',
						icon: '⚙',
						title: task.task_name,
						subtitle: `${exec.workflow_name} · ${task.status}`,
						action: () => {
							emit('navigate-execution', exec.id)
							close()
						},
					})
				}
			}
		}

		return out.slice(0, 12)
	})

	watch(results, () => {
		selectedIndex.value = 0
	})

	function open() {
		isOpen.value = true
		nextTick(() => inputEl.value?.focus())
	}

	function close() {
		isOpen.value = false
		query.value = ''
		selectedIndex.value = 0
	}

	function moveSelection(delta: number) {
		const max = results.value.length - 1
		selectedIndex.value = Math.min(max, Math.max(0, selectedIndex.value + delta))
	}

	function selectCurrent() {
		results.value[selectedIndex.value]?.action()
	}

	function select(result: SearchResult) {
		result.action()
	}

	function highlight(text: string): string {
		if (!query.value) return text
		const escaped = query.value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
		return text.replace(
			new RegExp(escaped, 'gi'),
			(m) =>
				`<mark style="background:rgba(124,106,255,.3);color:inherit;border-radius:2px">${m}</mark>`,
		)
	}

	// Global keyboard shortcut
	function onKeydown(e: KeyboardEvent) {
		if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
			e.preventDefault()
			if (isOpen.value) close()
			else open()
		}
	}
	onMounted(() => document.addEventListener('keydown', onKeydown))
	onUnmounted(() => document.removeEventListener('keydown', onKeydown))
</script>

<style scoped>
	.ws-trigger {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 5px 12px;
		border-radius: var(--radius-sm);
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text3);
		font-size: 12px;
		cursor: pointer;
		transition: all 0.15s;
	}
	.ws-trigger:hover {
		color: var(--text2);
		border-color: var(--border2);
	}
	.ws-icon {
		font-size: 14px;
	}
	.ws-hint {
		display: none;
	}
	@media (min-width: 900px) {
		.ws-hint {
			display: inline;
		}
	}
	.ws-kbd {
		font-size: 10px;
		padding: 1px 5px;
		background: var(--bg3);
		border: 1px solid var(--border2);
		border-radius: 4px;
		color: var(--text3);
		font-family: var(--mono);
	}

	.ws-overlay {
		position: fixed;
		inset: 0;
		z-index: 9998;
		background: rgba(0, 0, 0, 0.6);
		backdrop-filter: blur(3px);
		display: flex;
		align-items: flex-start;
		justify-content: center;
		padding-top: 80px;
	}

	.ws-modal {
		width: 580px;
		max-width: 95vw;
		background: var(--bg2);
		border: 1px solid var(--border2);
		border-radius: var(--radius-lg);
		overflow: hidden;
		box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5);
	}

	.ws-input-wrap {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 14px 16px;
		border-bottom: 1px solid var(--border);
	}
	.ws-search-icon {
		font-size: 16px;
		color: var(--text3);
	}
	.ws-input {
		flex: 1;
		background: none;
		border: none;
		outline: none;
		color: var(--text);
		font-size: 15px;
		font-family: var(--sans);
	}
	.ws-input::placeholder {
		color: var(--text3);
	}
	.ws-clear {
		background: none;
		border: none;
		color: var(--text3);
		font-size: 14px;
		cursor: pointer;
		padding: 2px;
	}

	.ws-results {
		max-height: 360px;
		overflow-y: auto;
	}
	.ws-results::-webkit-scrollbar {
		width: 4px;
	}
	.ws-results::-webkit-scrollbar-thumb {
		background: var(--border2);
	}

	.ws-result {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 10px 16px;
		cursor: pointer;
		border-bottom: 1px solid var(--border);
		transition: background 0.1s;
	}
	.ws-result:hover,
	.ws-result.selected {
		background: rgba(124, 106, 255, 0.1);
	}
	.ws-result-icon {
		font-size: 16px;
		width: 22px;
		text-align: center;
		flex-shrink: 0;
		color: var(--text3);
	}
	.ws-result-body {
		flex: 1;
		min-width: 0;
	}
	.ws-result-title {
		font-size: 13px;
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.ws-result-sub {
		font-size: 11px;
		color: var(--text3);
		margin-top: 2px;
	}
	.ws-result-type {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--text3);
		background: var(--surface);
		padding: 2px 6px;
		border-radius: 4px;
		flex-shrink: 0;
	}

	.ws-empty {
		padding: 24px 16px;
		text-align: center;
		color: var(--text3);
		font-size: 13px;
	}
	.ws-empty em {
		color: var(--text2);
		font-style: normal;
	}

	.ws-footer {
		display: flex;
		gap: 16px;
		padding: 8px 16px;
		border-top: 1px solid var(--border);
		background: var(--bg3);
		font-size: 10px;
		color: var(--text3);
	}
	.ws-footer kbd {
		background: var(--surface);
		border: 1px solid var(--border2);
		border-radius: 3px;
		padding: 0 4px;
		font-family: var(--mono);
		font-size: 10px;
		color: var(--text2);
	}

	.modal-enter-active,
	.modal-leave-active {
		transition: all 0.15s ease;
	}
	.modal-enter-from,
	.modal-leave-to {
		opacity: 0;
	}
	.modal-enter-from .ws-modal,
	.modal-leave-to .ws-modal {
		transform: translateY(-8px) scale(0.98);
	}
</style>
