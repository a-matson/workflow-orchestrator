<template>
	<span class="status-badge" :style="{ background: bg, color: color, borderColor: color + '44' }">
		<span v-if="status === 'running'" class="pulse-dot"></span>
		{{ status.replace('_', ' ') }}
	</span>
</template>

<script setup lang="ts">
	import { computed } from 'vue'
	import { STATUS_COLORS, STATUS_BG } from '../../types'
	import type { TaskStatus, WorkflowStatus } from '../../types'

	const props = defineProps<{ status: TaskStatus | WorkflowStatus }>()
	const color = computed(() => STATUS_COLORS[props.status] || '#6b7280')
	const bg = computed(() => STATUS_BG[props.status] || '#f3f4f6')
</script>

<style scoped>
	.status-badge {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 2px 9px;
		border-radius: 20px;
		font-size: 11px;
		font-weight: 600;
		text-transform: capitalize;
		border: 1px solid;
		white-space: nowrap;
	}

	.pulse-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: currentColor;
		animation: pulse 1.2s ease-in-out infinite;
	}

	@keyframes pulse {
		0%,
		100% {
			opacity: 1;
			transform: scale(1);
		}
		50% {
			opacity: 0.5;
			transform: scale(0.8);
		}
	}
</style>
