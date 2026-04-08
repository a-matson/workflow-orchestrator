<template>
	<div class="template-selector">
		<div class="ts-header">
			<span class="ts-title">Templates</span>
			<button class="ts-close" @click="$emit('close')">✕</button>
		</div>

		<div class="ts-list">
			<div
				v-for="(tpl, i) in WORKFLOW_TEMPLATES"
				:key="i"
				class="ts-item"
				@click="selectTemplate(tpl)"
			>
				<div class="ts-item-icon">
					{{ ICONS[tpl.tags?.category ?? ''] ?? '⬡' }}
				</div>
				<div class="ts-item-body">
					<div class="ts-item-name">
						{{ tpl.name }}
					</div>
					<div class="ts-item-desc">
						{{ tpl.description }}
					</div>
					<div class="ts-item-meta">
						<span class="ts-badge">{{ tpl.tasks.length }} tasks</span>
						<span class="ts-badge">{{ tpl.tags?.category ?? 'general' }}</span>
					</div>
				</div>
			</div>
		</div>
	</div>
</template>

<script setup lang="ts">
	import { WORKFLOW_TEMPLATES } from '../../composables/useTemplates'
	import type { WorkflowDefinition } from '../../types'

	const emit = defineEmits<{
		(e: 'select', tpl: Omit<WorkflowDefinition, 'id' | 'created_at' | 'updated_at'>): void
		(e: 'close'): void
	}>()

	const ICONS: Record<string, string> = {
		data: '⚡',
		ml: '🧠',
		devops: '🚀',
		reporting: '📊',
	}

	function selectTemplate(tpl: (typeof WORKFLOW_TEMPLATES)[number]) {
		emit('select', tpl)
	}
</script>

<style scoped>
	.template-selector {
		display: flex;
		flex-direction: column;
		background: var(--bg2);
		border-right: 1px solid var(--border);
		height: 100%;
	}

	.ts-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 12px 14px;
		border-bottom: 1px solid var(--border);
	}
	.ts-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--text2);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}
	.ts-close {
		background: none;
		border: none;
		color: var(--text3);
		font-size: 14px;
		cursor: pointer;
		padding: 2px;
	}
	.ts-close:hover {
		color: var(--text);
	}

	.ts-list {
		flex: 1;
		overflow-y: auto;
		padding: 8px 0;
	}
	.ts-list::-webkit-scrollbar {
		width: 3px;
	}
	.ts-list::-webkit-scrollbar-thumb {
		background: var(--border2);
	}

	.ts-item {
		display: flex;
		gap: 10px;
		align-items: flex-start;
		padding: 10px 14px;
		cursor: pointer;
		border-bottom: 1px solid var(--border);
		transition: background 0.1s;
	}
	.ts-item:hover {
		background: var(--bg3);
	}

	.ts-item-icon {
		font-size: 22px;
		flex-shrink: 0;
		margin-top: 2px;
	}

	.ts-item-body {
		flex: 1;
		min-width: 0;
	}
	.ts-item-name {
		font-size: 12px;
		font-weight: 600;
		margin-bottom: 3px;
	}
	.ts-item-desc {
		font-size: 11px;
		color: var(--text3);
		line-height: 1.4;
		margin-bottom: 6px;
	}

	.ts-item-meta {
		display: flex;
		gap: 5px;
		flex-wrap: wrap;
	}
	.ts-badge {
		font-size: 9px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--surface);
		color: var(--text3);
		border: 1px solid var(--border2);
	}
</style>
