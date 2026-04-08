<template>
  <div class="timeline">
    <div class="timeline-header">
      <span class="tl-title">Execution Timeline</span>
      <span class="tl-range">{{ totalDuration }}</span>
    </div>

    <!-- Time axis -->
    <div class="tl-axis">
      <div
        v-for="tick in timeTicks"
        :key="tick.label"
        class="tl-tick"
        :style="{ left: tick.pct + '%' }"
      >
        <div class="tick-line"></div>
        <span class="tick-label">{{ tick.label }}</span>
      </div>
    </div>

    <!-- Task rows -->
    <div class="tl-rows">
      <div
        v-for="task in sortedTasks"
        :key="task.id"
        class="tl-row"
        :class="{ 'tl-row-active': task.id === hoveredTaskId }"
        @mouseenter="hoveredTaskId = task.id"
        @mouseleave="hoveredTaskId = null"
      >
        <div
          class="tl-row-label"
          :title="task.task_name"
        >
          {{ task.task_name }}
        </div>
        <div class="tl-row-track">
          <!-- Pending region (before queue) -->
          <div
            v-if="task.queued_at"
            class="tl-segment pending"
            :style="segmentStyle(task, 'pending')"
            :title="`Pending: ${segmentDuration(task, 'pending')}`"
          ></div>
          <!-- Queue wait (queued → running) -->
          <div
            v-if="task.queued_at && task.started_at"
            class="tl-segment queued"
            :style="segmentStyle(task, 'queued')"
            :title="`Queue wait: ${segmentDuration(task, 'queued')}`"
          ></div>
          <!-- Execution (running → completed/failed) -->
          <div
            v-if="task.started_at"
            class="tl-segment"
            :class="task.status"
            :style="segmentStyle(task, 'running')"
            :title="`${task.status}: ${segmentDuration(task, 'running')}`"
          ></div>
        </div>
        <div class="tl-row-dur">
          {{ taskDuration(task) }}
        </div>
      </div>
    </div>

    <!-- Hover tooltip -->
    <Transition name="fade">
      <div
        v-if="hoveredTask"
        class="tl-tooltip"
      >
        <div class="tt-name">
          {{ hoveredTask.task_name }}
        </div>
        <div class="tt-row">
          <span>Status</span><StatusBadge :status="hoveredTask.status" />
        </div>
        <div class="tt-row">
          <span>Worker</span><span>{{ hoveredTask.worker_id?.slice(-8) || '—' }}</span>
        </div>
        <div class="tt-row">
          <span>Retries</span><span>{{ hoveredTask.retry_count }}/{{ hoveredTask.max_retries }}</span>
        </div>
        <div
          v-if="hoveredTask.started_at"
          class="tt-row"
        >
          <span>Started</span><span>{{ fmt(hoveredTask.started_at) }}</span>
        </div>
        <div
          v-if="hoveredTask.completed_at"
          class="tt-row"
        >
          <span>Completed</span><span>{{ fmt(hoveredTask.completed_at) }}</span>
        </div>
        <div
          v-if="hoveredTask.error"
          class="tt-error"
        >
          {{ hoveredTask.error }}
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import type { WorkflowExecution, TaskExecution } from '../../types'
import StatusBadge from '../shared/StatusBadge.vue'

const props = defineProps<{ execution: WorkflowExecution }>()

const hoveredTaskId = ref<string | null>(null)

const hoveredTask = computed(
  () => props.execution.tasks?.find((t) => t.id === hoveredTaskId.value) ?? null,
)

// ── Time boundaries ───────────────────────────────────────────
const timelineStart = computed(() => {
  if (!props.execution.started_at) return Date.now() - 60000
  return new Date(props.execution.started_at).getTime()
})

const timelineEnd = computed(() => {
  const end = props.execution.completed_at
    ? new Date(props.execution.completed_at).getTime()
    : Date.now()
  return Math.max(end, timelineStart.value + 1000)
})

const timelineDuration = computed(() => timelineEnd.value - timelineStart.value)

const totalDuration = computed(() => {
  const ms = timelineDuration.value
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
})

// ── Tasks sorted by start time ────────────────────────────────
const sortedTasks = computed(() => {
  return [...(props.execution.tasks ?? [])].sort((a, b) => {
    const at = a.queued_at ? new Date(a.queued_at).getTime() : Infinity
    const bt = b.queued_at ? new Date(b.queued_at).getTime() : Infinity
    return at - bt
  })
})

// ── Time ticks ────────────────────────────────────────────────
const timeTicks = computed(() => {
  const duration = timelineDuration.value
  const numTicks = 5
  return Array.from({ length: numTicks + 1 }, (_, i) => {
    const ms = (duration / numTicks) * i
    return {
      pct: (i / numTicks) * 100,
      label: formatMs(ms),
    }
  })
})

function formatMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(0)}s`
  return `${Math.floor(ms / 60000)}m`
}

// ── Segment geometry ──────────────────────────────────────────
function pct(ts: string | undefined | null): number {
  if (!ts) return 0
  const elapsed = new Date(ts).getTime() - timelineStart.value
  return Math.min(100, Math.max(0, (elapsed / timelineDuration.value) * 100))
}

function segmentStyle(task: TaskExecution, phase: 'pending' | 'queued' | 'running') {
  let left = 0
  let right = 100

  if (phase === 'pending') {
    left = 0
    right = pct(task.queued_at)
  } else if (phase === 'queued') {
    left = pct(task.queued_at)
    right = pct(task.started_at)
  } else {
    left = pct(task.started_at)
    right = task.completed_at ? pct(task.completed_at) : 100
  }

  const width = Math.max(0, right - left)
  return { left: `${left}%`, width: `${width}%` }
}

function segmentDuration(task: TaskExecution, phase: 'pending' | 'queued' | 'running'): string {
  let start: string | undefined
  let end: string | undefined
  if (phase === 'pending') {
    start = props.execution.started_at ?? undefined
    end = task.queued_at ?? undefined
  } else if (phase === 'queued') {
    start = task.queued_at ?? undefined
    end = task.started_at ?? undefined
  } else {
    start = task.started_at ?? undefined
    end = task.completed_at ?? undefined
  }

  if (!start) return '—'
  const ms = (end ? new Date(end) : new Date()).getTime() - new Date(start).getTime()
  return formatMs(ms)
}

function taskDuration(task: TaskExecution): string {
  if (!task.started_at) return '—'
  const ms =
    (task.completed_at ? new Date(task.completed_at) : new Date()).getTime() -
    new Date(task.started_at).getTime()
  return formatMs(ms)
}

function fmt(ts: string): string {
  return new Date(ts).toLocaleTimeString()
}
</script>

<style scoped>
.timeline {
  display: flex;
  flex-direction: column;
  gap: 0;
  font-size: 12px;
}

.timeline-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}
.tl-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text2);
}
.tl-range {
  font-size: 11px;
  color: var(--text3);
  font-family: var(--mono);
}

/* Time axis */
.tl-axis {
  position: relative;
  height: 24px;
  margin: 0 120px 0 160px;
  border-bottom: 1px solid var(--border2);
}
.tl-tick {
  position: absolute;
  top: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
}
.tick-line {
  width: 1px;
  height: 8px;
  background: var(--border2);
}
.tick-label {
  font-size: 9px;
  color: var(--text3);
  font-family: var(--mono);
  white-space: nowrap;
  margin-top: 2px;
}

/* Task rows */
.tl-rows {
  display: flex;
  flex-direction: column;
  overflow-y: auto;
  max-height: 320px;
}
.tl-rows::-webkit-scrollbar {
  width: 4px;
}
.tl-rows::-webkit-scrollbar-thumb {
  background: var(--border2);
}

.tl-row {
  display: flex;
  align-items: center;
  height: 32px;
  border-bottom: 1px solid var(--border);
  transition: background 0.1s;
}
.tl-row-active {
  background: rgba(124, 106, 255, 0.05);
}

.tl-row-label {
  width: 160px;
  flex-shrink: 0;
  padding: 0 12px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--text2);
}

.tl-row-track {
  flex: 1;
  position: relative;
  height: 20px;
  background: var(--surface2);
  border-radius: 3px;
  overflow: hidden;
}

.tl-segment {
  position: absolute;
  top: 0;
  height: 100%;
  border-radius: 2px;
  transition: all 0.3s;
  min-width: 2px;
}
.tl-segment.pending {
  background: rgba(144, 144, 168, 0.2);
}
.tl-segment.queued {
  background: rgba(192, 132, 252, 0.4);
}
.tl-segment.running {
  background: rgba(59, 158, 255, 0.8);
  animation: shimmer 1.5s infinite;
}
.tl-segment.completed {
  background: rgba(34, 211, 160, 0.8);
}
.tl-segment.failed {
  background: rgba(255, 95, 87, 0.8);
}
.tl-segment.retrying {
  background: rgba(245, 166, 35, 0.7);
}
.tl-segment.dead_letter {
  background: rgba(197, 48, 48, 0.8);
}

@keyframes shimmer {
  0%,
  100% {
    opacity: 0.8;
  }
  50% {
    opacity: 1;
  }
}

.tl-row-dur {
  width: 56px;
  flex-shrink: 0;
  text-align: right;
  padding-right: 12px;
  font-size: 10px;
  color: var(--text3);
  font-family: var(--mono);
}

/* Tooltip */
.tl-tooltip {
  position: absolute;
  right: 12px;
  bottom: 12px;
  background: var(--surface);
  border: 1px solid var(--border2);
  border-radius: var(--radius-lg);
  padding: 10px 14px;
  min-width: 200px;
  z-index: 100;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
}
.tt-name {
  font-size: 12px;
  font-weight: 600;
  margin-bottom: 8px;
}
.tt-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 11px;
  margin-bottom: 4px;
}
.tt-row span:first-child {
  color: var(--text3);
}
.tt-row span:last-child {
  font-family: var(--mono);
  color: var(--text2);
}
.tt-error {
  margin-top: 6px;
  font-size: 10px;
  color: var(--red);
  font-family: var(--mono);
  word-break: break-all;
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.15s;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
