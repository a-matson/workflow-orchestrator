<template>
  <div class="exec-progress-widget">
    <div class="epw-header">
      <span class="epw-name">{{ execution.workflow_name }}</span>
      <StatusBadge :status="execution.status" />
      <span class="epw-elapsed">{{ elapsed }}</span>
    </div>

    <!-- Segmented progress bar: one segment per task -->
    <div
      class="epw-bar"
      :title="`${completedCount}/${totalCount} tasks complete`"
    >
      <div
        v-for="task in execution.tasks"
        :key="task.id"
        class="epw-segment"
        :style="{ background: segmentColor(task.status), flex: 1 }"
        :title="`${task.task_name}: ${task.status}`"
      ></div>
    </div>

    <!-- Stats row -->
    <div class="epw-stats">
      <span
        v-for="stat in stats"
        :key="stat.label"
        class="epw-stat"
        :style="{ color: stat.color }"
      >
        {{ stat.count }} {{ stat.label }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import type { WorkflowExecution } from '../../types'
import { STATUS_COLORS } from '../../types'
import StatusBadge from '../shared/StatusBadge.vue'

const props = defineProps<{ execution: WorkflowExecution }>()

const now = ref(Date.now())
let timer: ReturnType<typeof setInterval>
onMounted(() => {
  timer = setInterval(() => (now.value = Date.now()), 1000)
})
onUnmounted(() => clearInterval(timer))

const totalCount = computed(() => props.execution.tasks?.length ?? 0)
const completedCount = computed(
  () => props.execution.tasks?.filter((t) => t.status === 'completed').length ?? 0,
)

const elapsed = computed(() => {
  if (!props.execution.started_at) return ''
  const ms =
    (props.execution.completed_at
      ? new Date(props.execution.completed_at)
      : new Date(now.value)
    ).getTime() - new Date(props.execution.started_at).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
})

const stats = computed(() => {
  const counts: Record<string, number> = {}
  for (const t of props.execution.tasks ?? []) {
    counts[t.status] = (counts[t.status] ?? 0) + 1
  }
  return Object.entries(counts)
    .filter(([, v]) => v > 0)
    .map(([status, count]) => ({
      label: status,
      count,
      color: STATUS_COLORS[status as keyof typeof STATUS_COLORS] ?? '#9090a8',
    }))
})

function segmentColor(status: string): string {
  return STATUS_COLORS[status as keyof typeof STATUS_COLORS] ?? '#2e2e48'
}
</script>

<style scoped>
.exec-progress-widget {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.epw-header {
  display: flex;
  align-items: center;
  gap: 8px;
}
.epw-name {
  font-size: 12px;
  font-weight: 600;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.epw-elapsed {
  font-size: 10px;
  color: var(--text3);
  font-family: var(--mono);
  flex-shrink: 0;
}

.epw-bar {
  display: flex;
  height: 6px;
  border-radius: 3px;
  overflow: hidden;
  background: var(--surface2);
  gap: 1px;
}
.epw-segment {
  min-width: 2px;
  transition: background 0.3s;
}

.epw-stats {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.epw-stat {
  font-size: 10px;
  font-family: var(--mono);
}
</style>
