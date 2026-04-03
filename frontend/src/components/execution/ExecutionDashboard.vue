<template>
  <div class="execution-dashboard">
    <!-- Header row -->
    <div class="exec-header">
      <div class="exec-info">
        <span class="exec-id">{{ execution.id.slice(0, 8) }}…</span>
        <span class="exec-name">{{ execution.workflow_name }}</span>
        <StatusBadge :status="execution.status" />
      </div>
      <div class="exec-actions">
        <button @click="$emit('retry')" v-if="execution.status === 'failed'" class="btn-retry">
          ↺ Retry
        </button>
        <button @click="$emit('cancel')" v-if="execution.status === 'running'" class="btn-cancel">
          ◼ Cancel
        </button>
        <button @click="expanded = !expanded" class="btn-toggle">
          {{ expanded ? '▲ Collapse' : '▼ Expand' }}
        </button>
      </div>
    </div>

    <!-- Progress bar -->
    <div class="progress-track">
      <div
        class="progress-fill"
        :class="execution.status"
        :style="{ width: progressPercent + '%' }"
      ></div>
    </div>

    <!-- Task metrics row -->
    <div class="task-metrics">
      <div
        v-for="stat in taskStats"
        :key="stat.status"
        class="stat-chip"
        :style="{ borderColor: stat.color, color: stat.color, background: stat.bg }"
      >
        <span class="stat-count">{{ stat.count }}</span>
        <span class="stat-label">{{ stat.status }}</span>
      </div>
      <div class="stat-chip timing">
        <span class="stat-count">{{ elapsed }}</span>
        <span class="stat-label">elapsed</span>
      </div>
    </div>

    <!-- DAG graph visualization -->
    <div v-if="expanded" class="dag-visualization">
      <VueFlow
        :nodes="dagNodes"
        :edges="dagEdges"
        :nodeTypes="nodeTypes"
        :edges-updatable="false"
        :nodes-connectable="false"
        :elements-selectable="true"
        fit-view-on-init
        @node-click="onNodeClick"
        class="exec-flow"
      >
        <Background />
        <Controls :show-interactive="false" />
      </VueFlow>
    </div>

    <!-- Selected task detail drawer -->
    <Transition name="fade">
      <div v-if="selectedTask" class="task-detail">
        <div class="detail-header">
          <div class="detail-name">
            <StatusBadge :status="selectedTask.status" />
            <span>{{ selectedTask.task_name }}</span>
          </div>
          <button @click="selectedTask = null" class="close-btn">✕</button>
        </div>
        <div class="detail-body">
          <div class="detail-row">
            <span class="detail-key">Worker</span>
            <span class="detail-val">{{ selectedTask.worker_id || '—' }}</span>
          </div>
          <div class="detail-row">
            <span class="detail-key">Retries</span>
            <span class="detail-val"
              >{{ selectedTask.retry_count }} / {{ selectedTask.max_retries }}</span
            >
          </div>
          <div class="detail-row" v-if="selectedTask.started_at">
            <span class="detail-key">Started</span>
            <span class="detail-val">{{ formatTime(selectedTask.started_at) }}</span>
          </div>
          <div class="detail-row" v-if="selectedTask.completed_at">
            <span class="detail-key">Completed</span>
            <span class="detail-val">{{ formatTime(selectedTask.completed_at) }}</span>
          </div>
          <div v-if="selectedTask.error" class="error-block">
            <span class="detail-key">Error</span>
            <pre class="error-text">{{ selectedTask.error }}</pre>
          </div>
          <div v-if="selectedTask.logs?.length" class="logs-preview">
            <span class="detail-key">Logs ({{ selectedTask.logs.length }})</span>
            <div class="log-lines">
              <div
                v-for="(log, i) in selectedTask.logs.slice(-8)"
                :key="i"
                class="log-line"
                :class="log.level"
              >
                <span class="log-ts">{{ formatTime(log.timestamp) }}</span>
                <span class="log-level">{{ log.level }}</span>
                <span class="log-msg">{{ log.message }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, markRaw, onMounted, onUnmounted } from 'vue'
import { VueFlow } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import '@vue-flow/core/dist/style.css'
import type { WorkflowExecution, TaskExecution, TaskStatus } from '../../types'
import { STATUS_COLORS, STATUS_BG } from '../../types'
import TaskNode from '../dag/TaskNode.vue'
import StatusBadge from '../shared/StatusBadge.vue'

const props = defineProps<{
  execution: WorkflowExecution
  workflowTasks?: Array<{ id: string; dependencies: string[] }>
}>()
const emit = defineEmits<{
  (e: 'retry'): void
  (e: 'cancel'): void
  (e: 'task-click', task: TaskExecution): void
}>()

const expanded = ref(true)
const selectedTask = ref<TaskExecution | null>(null)
const nodeTypes = { taskNode: markRaw(TaskNode) }
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval>

onMounted(() => {
  timer = setInterval(() => (now.value = Date.now()), 1000)
})
onUnmounted(() => clearInterval(timer))

// const taskMap = computed(() => {
//   const m = new Map<string, TaskExecution>()
//   props.execution.tasks?.forEach(t => m.set(t.task_definition_id, t))
//   return m
// })

const progressPercent = computed(() => {
  const tasks = props.execution.tasks || []
  if (!tasks.length) return 0
  const done = tasks.filter((t) => t.status === 'completed').length
  return Math.round((done / tasks.length) * 100)
})

const taskStats = computed(() => {
  const counts: Partial<Record<TaskStatus, number>> = {}
  for (const t of props.execution.tasks || []) {
    counts[t.status] = (counts[t.status] || 0) + 1
  }
  return Object.entries(counts).map(([status, count]) => ({
    status,
    count,
    color: STATUS_COLORS[status as TaskStatus],
    bg: STATUS_BG[status as TaskStatus],
  }))
})

const elapsed = computed(() => {
  if (!props.execution.started_at) return '—'
  const start = new Date(props.execution.started_at).getTime()
  const end = props.execution.completed_at
    ? new Date(props.execution.completed_at).getTime()
    : now.value
  const ms = end - start
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
})

// Build DAG nodes/edges from execution tasks
const dagNodes = computed(() => {
  const tasks = props.execution.tasks || []
  return tasks.map((t, i) => {
    // Auto-layout: group by dependency depth
    const col = i % 3
    const row = Math.floor(i / 3)
    return {
      id: t.task_definition_id,
      type: 'taskNode',
      position: { x: col * 230 + 20, y: row * 160 + 20 },
      data: {
        taskDef: {
          id: t.task_definition_id,
          name: t.task_name,
          type: t.task_type,
          dependencies: [],
          config: {},
        },
        status: t.status,
        taskExec: t,
      },
    }
  })
})

const dagEdges = computed(() => {
  const edges: Array<{
    id: string
    source: string
    target: string
    type: string
    animated: boolean
    style: Record<string, string>
  }> = []
  for (const t of props.execution.tasks || []) {
    // We need the original DAG to know edges — use workflow tasks prop if available
    const origTask = props.workflowTasks?.find((wt) => wt.id === t.task_definition_id)
    if (origTask) {
      for (const dep of origTask.dependencies) {
        edges.push({
          id: `e-${dep}-${t.task_definition_id}`,
          source: dep,
          target: t.task_definition_id,
          type: 'smoothstep',
          animated: t.status === 'running',
          style: { stroke: STATUS_COLORS[t.status as TaskStatus], strokeWidth: '2' },
        })
      }
    }
  }
  return edges
})

function onNodeClick({ node }: { node: { id: string } }) {
  const task = (props.execution.tasks || []).find((t) => t.task_definition_id === node.id)
  if (task) {
    selectedTask.value = task
    emit('task-click', task)
  }
}

function formatTime(ts: string) {
  return new Date(ts).toLocaleTimeString()
}
</script>

<style scoped>
.execution-dashboard {
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  overflow: hidden;
  position: relative;
}

.exec-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  gap: 12px;
}

.exec-info {
  display: flex;
  align-items: center;
  gap: 10px;
}
.exec-id {
  font-family: monospace;
  font-size: 11px;
  color: #9ca3af;
}
.exec-name {
  font-size: 14px;
  font-weight: 600;
  color: #111827;
}

.exec-actions {
  display: flex;
  gap: 8px;
}
.btn-retry {
  padding: 5px 12px;
  background: #fef3c7;
  color: #b45309;
  border: 1px solid #fde68a;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
}
.btn-cancel {
  padding: 5px 12px;
  background: #fee2e2;
  color: #dc2626;
  border: 1px solid #fca5a5;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
}
.btn-toggle {
  padding: 5px 12px;
  background: #f9fafb;
  color: #6b7280;
  border: 1px solid #e5e7eb;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
}

.progress-track {
  height: 3px;
  background: #f3f4f6;
  margin: 0;
}
.progress-fill {
  height: 100%;
  border-radius: 2px;
  transition: width 0.5s ease;
  background: #4f46e5;
}
.progress-fill.completed {
  background: #10b981;
}
.progress-fill.failed {
  background: #ef4444;
}
.progress-fill.running {
  background: #3b82f6;
  animation: shimmer 1.5s infinite;
}

@keyframes shimmer {
  0% {
    opacity: 0.8;
  }
  50% {
    opacity: 1;
  }
  100% {
    opacity: 0.8;
  }
}

.task-metrics {
  display: flex;
  gap: 8px;
  padding: 10px 16px;
  flex-wrap: wrap;
  background: #fafafa;
  border-top: 1px solid #f3f4f6;
  border-bottom: 1px solid #f3f4f6;
}

.stat-chip {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 3px 10px;
  border-radius: 20px;
  font-size: 11px;
  border: 1px solid;
}
.stat-count {
  font-weight: 700;
  font-size: 13px;
}
.stat-label {
  text-transform: capitalize;
}
.stat-chip.timing {
  background: #f8f9fa;
  color: #374151;
  border-color: #e5e7eb;
}

.dag-visualization {
  height: 340px;
  border-top: 1px solid #f3f4f6;
}

.exec-flow {
  background: #fafafa;
}

/* Task detail panel */
.task-detail {
  border-top: 1px solid #e5e7eb;
  background: #fff;
}

.detail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid #f3f4f6;
}
.detail-name {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
}
.close-btn {
  background: none;
  border: none;
  cursor: pointer;
  color: #9ca3af;
  font-size: 16px;
}

.detail-body {
  padding: 12px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.detail-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.detail-key {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: #9ca3af;
}
.detail-val {
  font-size: 12px;
  color: #374151;
  font-family: monospace;
}

.error-block {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.error-text {
  margin: 0;
  font-size: 11px;
  background: #fef2f2;
  color: #dc2626;
  padding: 8px;
  border-radius: 6px;
  white-space: pre-wrap;
  word-break: break-all;
}

.logs-preview {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.log-lines {
  background: #111827;
  border-radius: 8px;
  padding: 8px;
  max-height: 160px;
  overflow-y: auto;
}
.log-line {
  display: flex;
  gap: 6px;
  font-family: monospace;
  font-size: 10px;
  line-height: 1.6;
}
.log-ts {
  color: #6b7280;
  flex-shrink: 0;
}
.log-level {
  flex-shrink: 0;
  font-weight: 700;
}
.log-line.info .log-level {
  color: #60a5fa;
}
.log-line.warn .log-level {
  color: #fbbf24;
}
.log-line.error .log-level {
  color: #f87171;
}
.log-msg {
  color: #d1d5db;
  word-break: break-all;
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
