<template>
  <div v-if="result.issues.length > 0 || showStats" class="validation-panel">
    <!-- Issues list -->
    <div
      v-for="issue in result.issues"
      :key="issue.code + (issue.taskId ?? '')"
      class="issue-row"
      :class="issue.type"
    >
      <span class="issue-icon">{{ issue.type === 'error' ? '✗' : '⚠' }}</span>
      <span class="issue-msg">{{ issue.message }}</span>
    </div>

    <!-- Stats bar (shown when valid) -->
    <div v-if="showStats && result.valid" class="stats-bar">
      <span class="stat-item"><span class="stat-val">{{ result.stats.taskCount }}</span> tasks</span>
      <span class="stat-sep">·</span>
      <span class="stat-item"><span class="stat-val">{{ result.stats.edgeCount }}</span> edges</span>
      <span class="stat-sep">·</span>
      <span class="stat-item"><span class="stat-val">{{ result.stats.rootCount }}</span> roots</span>
      <span class="stat-sep">·</span>
      <span class="stat-item"><span class="stat-val">{{ result.stats.maxDepth }}</span> levels deep</span>
      <span class="stat-sep valid-check">✓ valid DAG</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { ValidationResult } from '../../composables/useWorkflowValidator'

defineProps<{
  result: ValidationResult
  showStats?: boolean
}>()
</script>

<style scoped>
.validation-panel { display: flex; flex-direction: column; gap: 2px; padding: 0 16px 8px; }

.issue-row {
  display: flex; align-items: center; gap: 7px;
  padding: 5px 10px; border-radius: var(--radius-sm);
  font-size: 11px;
}
.issue-row.error { background: rgba(255,95,87,.08); color: var(--red); }
.issue-row.warning { background: rgba(245,166,35,.08); color: var(--amber); }
.issue-icon { flex-shrink: 0; font-size: 11px; }
.issue-msg { flex: 1; }

.stats-bar {
  display: flex; align-items: center; gap: 6px;
  padding: 5px 10px; font-size: 10px;
  color: var(--text3); font-family: var(--mono);
}
.stat-val { color: var(--text2); font-weight: 600; }
.stat-sep { color: var(--border2); }
.valid-check { color: var(--green); font-weight: 600; }
</style>
