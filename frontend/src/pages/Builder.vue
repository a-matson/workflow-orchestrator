<template>
	<div class="builder-page">
		<!-- Sidebar: workflow list -->
		<aside class="wf-sidebar">
			<div class="sidebar-header">
				<span class="sidebar-title">Workflows</span>
				<button class="btn-new" title="New workflow" @click="createNew">+</button>
			</div>

			<div v-if="store.loading" class="sidebar-loading">Loading…</div>

			<div class="wf-list">
				<div
					v-for="wf in store.definitions"
					:key="wf.id"
					class="wf-item"
					:class="{ active: activeWorkflowId === wf.id }"
					@click="loadWorkflow(wf)"
				>
					<div class="wf-item-name">
						{{ wf.name }}
					</div>
					<div class="wf-item-meta">
						<span>{{ wf.tasks?.length || 0 }} tasks</span>
						<span class="wf-item-ver">v{{ wf.version }}</span>
					</div>
					<div class="wf-item-actions">
						<button class="wf-run-btn" title="Run this workflow" @click.stop="runWorkflow(wf.id)">
							▶
						</button>
					</div>
				</div>

				<div v-if="!store.loading && !store.definitions.length" class="sidebar-empty">
					No workflows yet — create one to get started
				</div>
			</div>

			<div v-if="store.error" class="sidebar-error">
				{{ store.error }}
			</div>
		</aside>

		<!-- Editor canvas -->
		<div class="editor-area">
			<DAGEditor ref="editorRef" @triggered="onTriggered" />
		</div>
	</div>
</template>

<script setup lang="ts">
	import { ref, onMounted, inject } from 'vue'
	import { useRouter } from 'vue-router'
	import { useWorkflowStore } from '../stores/workflow'
	import { useWebSocketStore } from '../stores/websocket'
	import type { WorkflowDefinition } from '../types'
	import DAGEditor from '../components/dag/DAGEditor.vue'

	const store = useWorkflowStore()
	const wsStore = useWebSocketStore()
	const router = useRouter()
	const showToast = inject<(msg: string, type?: 'success' | 'error' | 'info') => void>('showToast')

	const editorRef = ref<InstanceType<typeof DAGEditor> | null>(null)
	const activeWorkflowId = ref<string | null>(null)

	onMounted(async () => {
		await store.fetchDefinitions()
	})

	function createNew() {
		activeWorkflowId.value = null
		editorRef.value?.resetToEmpty()
	}

	function loadWorkflow(wf: WorkflowDefinition) {
		activeWorkflowId.value = wf.id
		editorRef.value?.loadWorkflow(wf.name, wf.tasks)
	}

	async function runWorkflow(wfId: string) {
		try {
			const exec = await store.triggerWorkflow(wfId, {})
			wsStore.subscribe(exec.id)
			showToast?.(`▶ Run started: ${exec.id.slice(0, 8)}…`, 'success')
			router.push({ name: 'execution-detail', params: { execId: exec.id } })
		} catch {
			showToast?.('Failed to trigger workflow', 'error')
		}
	}

	function onTriggered(execId: string) {
		wsStore.subscribe(execId)
		showToast?.('▶ Workflow triggered', 'success')
		router.push({ name: 'execution-detail', params: { execId } })
	}
</script>

<style scoped>
	.builder-page {
		display: flex;
		height: 100%;
		overflow: hidden;
	}

	/* Sidebar */
	.wf-sidebar {
		width: 220px;
		flex-shrink: 0;
		background: var(--bg2);
		border-right: 1px solid var(--border);
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}

	.sidebar-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
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
	.btn-new {
		width: 22px;
		height: 22px;
		border-radius: 5px;
		border: 1px solid var(--border2);
		background: var(--surface);
		color: var(--text2);
		font-size: 16px;
		line-height: 1;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.btn-new:hover {
		background: var(--surface2);
		color: var(--text);
	}

	.sidebar-loading {
		padding: 16px 14px;
		font-size: 12px;
		color: var(--text3);
	}
	.sidebar-empty {
		padding: 16px 14px;
		font-size: 11px;
		color: var(--text3);
		line-height: 1.5;
	}
	.sidebar-error {
		padding: 10px 14px;
		font-size: 11px;
		color: var(--red);
		border-top: 1px solid var(--border);
		flex-shrink: 0;
	}

	.wf-list {
		flex: 1;
		overflow-y: auto;
	}

	.wf-item {
		padding: 9px 14px;
		cursor: pointer;
		border-bottom: 1px solid var(--border);
		border-left: 2px solid transparent;
		transition: background 0.1s;
		position: relative;
	}
	.wf-item:hover {
		background: var(--bg3);
	}
	.wf-item.active {
		background: rgba(124, 106, 255, 0.08);
		border-left-color: var(--accent);
	}
	.wf-item:hover .wf-item-actions {
		opacity: 1;
	}

	.wf-item-name {
		font-size: 12px;
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		margin-bottom: 3px;
	}
	.wf-item-meta {
		display: flex;
		gap: 8px;
		font-size: 10px;
		color: var(--text3);
	}
	.wf-item-ver {
		color: var(--text3);
	}

	.wf-item-actions {
		position: absolute;
		right: 10px;
		top: 50%;
		transform: translateY(-50%);
		opacity: 0;
		transition: opacity 0.15s;
	}
	.wf-run-btn {
		width: 22px;
		height: 22px;
		border-radius: 4px;
		background: rgba(34, 211, 160, 0.12);
		border: none;
		color: var(--green);
		font-size: 9px;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.wf-run-btn:hover {
		background: rgba(34, 211, 160, 0.25);
	}

	/* Editor */
	.editor-area {
		flex: 1;
		overflow: hidden;
	}
</style>
