<template>
  <div
    class="task-node"
    :class="[`status-${data.status || 'pending'}`, { selected: selected }]"
  >
    <!-- Status indicator bar -->
    <div class="status-bar" :style="{ background: statusColor }"></div>

    <!-- Header -->
    <div class="node-header">
      <span class="task-type-badge">{{ taskTypeLabel }}</span>
      <span v-if="data.status" class="status-dot" :style="{ background: statusColor }"></span>
    </div>

    <!-- Name -->
    <div class="node-name">{{ data.taskDef.name }}</div>

    <!-- Meta row -->
    <div class="node-meta">
      <span v-if="data.taskExec?.retry_count && data.taskExec.retry_count > 0" class="retry-badge">
        ↺ {{ data.taskExec.retry_count }}
      </span>
      <span v-if="data.taskExec?.worker_id" class="worker-badge">
        {{ data.taskExec.worker_id.slice(-8) }}
      </span>
      <span v-if="duration" class="duration-badge">{{ duration }}</span>
    </div>

    <!-- Running pulse -->
    <div v-if="data.status === 'running'" class="running-pulse"></div>

    <!-- Handles rendered by VueFlow -->
    <Handle type="target" :position="Position.Top" />
    <Handle type="source" :position="Position.Bottom" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import type { NodeProps } from '@vue-flow/core'
import { STATUS_COLORS, TASK_TYPES } from '../../types'
import type { TaskDefinition, TaskExecution, TaskStatus } from '../../types'

interface NodeData {
  taskDef: TaskDefinition
  status?: TaskStatus
  taskExec?: TaskExecution
}

const props = defineProps<NodeProps<NodeData>>()
const { data, selected } = props

const statusColor = computed(() => STATUS_COLORS[data.status || 'pending'])

const taskTypeLabel = computed(() => {
  const found = TASK_TYPES.find(t => t.value === data.taskDef.type)
  return found ? found.label : data.taskDef.type
})

const duration = computed(() => {
  const exec = data.taskExec
  if (!exec?.started_at) return null
  const end = exec.completed_at ? new Date(exec.completed_at) : new Date()
  const ms = end.getTime() - new Date(exec.started_at).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
})
</script>

<style scoped>
.task-node {
  position: relative;
  background: var(--vf-node-bg, #fff);
  border: 1.5px solid #e5e7eb;
  border-radius: 10px;
  padding: 10px 14px 10px 14px;
  min-width: 160px;
  max-width: 200px;
  cursor: grab;
  transition: box-shadow 0.15s, border-color 0.15s;
  overflow: hidden;
}

.task-node.selected {
  border-color: #6366f1;
  box-shadow: 0 0 0 3px rgba(99,102,241,0.2);
}

.task-node:hover { box-shadow: 0 4px 16px rgba(0,0,0,0.1); }

.status-bar {
  position: absolute;
  top: 0; left: 0; right: 0;
  height: 3px;
  border-radius: 10px 10px 0 0;
}

.node-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 4px;
}

.task-type-badge {
  font-size: 9px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: #9ca3af;
}

.status-dot {
  width: 7px; height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}

.node-name {
  font-size: 13px;
  font-weight: 600;
  color: #111827;
  line-height: 1.3;
  margin-bottom: 6px;
  word-break: break-word;
}

.node-meta {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.retry-badge, .worker-badge, .duration-badge {
  font-size: 9px;
  padding: 1px 5px;
  border-radius: 4px;
  font-family: monospace;
}

.retry-badge { background: #fef3c7; color: #b45309; }
.worker-badge { background: #ede9fe; color: #6d28d9; }
.duration-badge { background: #f0fdf4; color: #166534; }

.running-pulse {
  position: absolute;
  inset: 0;
  border-radius: 10px;
  animation: pulse-border 1.5s ease-in-out infinite;
  pointer-events: none;
}

@keyframes pulse-border {
  0%, 100% { box-shadow: inset 0 0 0 1.5px rgba(59,130,246,0.4); }
  50% { box-shadow: inset 0 0 0 1.5px rgba(59,130,246,0.9); }
}

/* Status variants */
.status-completed { border-color: #86efac; }
.status-failed { border-color: #fca5a5; }
.status-running { border-color: #93c5fd; }
.status-dead_letter { border-color: #fca5a5; background: #fff5f5; }
</style>
