<template>
  <div class="log-viewer">
    <div class="log-toolbar">
      <div class="toolbar-left">
        <input v-model="searchQuery" placeholder="Search logs..." class="search-input" />
        <select v-model="levelFilter" class="level-filter">
          <option value="">All levels</option>
          <option value="info">Info</option>
          <option value="warn">Warn</option>
          <option value="error">Error</option>
          <option value="debug">Debug</option>
        </select>
        <select v-model="taskFilter" class="task-filter">
          <option value="">All tasks</option>
          <option v-for="task in execution.tasks" :key="task.id" :value="task.id">
            {{ task.task_name }}
          </option>
        </select>
      </div>
      <div class="toolbar-right">
        <span class="log-count">{{ filteredLogs.length }} entries</span>
        <label class="auto-scroll-toggle">
          <input type="checkbox" v-model="autoScroll" />
          Auto-scroll
        </label>
        <button @click="copyAll" class="btn-copy">Copy</button>
        <button @click="clearView" class="btn-clear">Clear</button>
      </div>
    </div>

    <div ref="logContainer" class="log-container" @scroll="onScroll">
      <div v-if="!filteredLogs.length" class="empty-state">
        <div class="empty-icon">📋</div>
        <div class="empty-text">
          {{ execution.status === 'running' ? 'Waiting for logs...' : 'No logs match your filter' }}
        </div>
      </div>

      <div
        v-for="(entry, i) in filteredLogs"
        :key="i"
        class="log-entry"
        :class="[entry.level, { new: entry._new }]"
      >
        <span class="log-ts">{{ formatTs(entry.timestamp) }}</span>
        <span class="log-level-badge" :class="entry.level">{{ entry.level?.toUpperCase() }}</span>
        <span class="log-task" v-if="entry._taskName">{{ entry._taskName }}</span>
        <span class="log-msg" v-html="highlight(entry.message)"></span>
        <span v-if="entry.fields && Object.keys(entry.fields).length" class="log-fields">
          {{ JSON.stringify(entry.fields) }}
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, nextTick } from 'vue'
import type { WorkflowExecution, LogEntry } from '../../types'

interface EnrichedLog extends LogEntry {
  _taskId: string
  _taskName: string
  _new?: boolean
}

const props = defineProps<{ execution: WorkflowExecution }>()

const searchQuery = ref('')
const levelFilter = ref('')
const taskFilter = ref('')
const autoScroll = ref(true)
const logContainer = ref<HTMLElement | null>(null)
const cleared = ref(false)

const allLogs = computed((): EnrichedLog[] => {
  if (cleared.value) return []
  const logs: EnrichedLog[] = []
  for (const task of (props.execution.tasks || [])) {
    for (const log of (task.logs || [])) {
      logs.push({ ...log, _taskId: task.id, _taskName: task.task_name })
    }
  }
  return logs.sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
})

const filteredLogs = computed(() => {
  return allLogs.value.filter(log => {
    if (levelFilter.value && log.level !== levelFilter.value) return false
    if (taskFilter.value && log._taskId !== taskFilter.value) return false
    if (searchQuery.value) {
      const q = searchQuery.value.toLowerCase()
      return log.message?.toLowerCase().includes(q) ||
        JSON.stringify(log.fields || {}).toLowerCase().includes(q)
    }
    return true
  })
})

watch(filteredLogs, async () => {
  if (!autoScroll.value) return
  await nextTick()
  if (logContainer.value) {
    logContainer.value.scrollTop = logContainer.value.scrollHeight
  }
})

function onScroll() {
  if (!logContainer.value) return
  const el = logContainer.value
  const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
  autoScroll.value = atBottom
}

function formatTs(ts: string) {
  if (!ts) return ''
  const d = new Date(ts)
  return `${d.toLocaleTimeString()}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

function highlight(msg: string) {
  if (!searchQuery.value || !msg) return msg
  const escaped = searchQuery.value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return msg.replace(new RegExp(escaped, 'gi'), m => `<mark class="highlight">${m}</mark>`)
}

function copyAll() {
  const text = filteredLogs.value.map(l => `[${formatTs(l.timestamp)}] ${l.level?.toUpperCase()} ${l._taskName}: ${l.message}`).join('\n')
  navigator.clipboard.writeText(text)
}

function clearView() {
  cleared.value = true
}
</script>

<style scoped>
.log-viewer {
  display: flex; flex-direction: column;
  height: 100%; background: #0d1117;
  border-radius: 10px; overflow: hidden;
}

.log-toolbar {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 12px;
  background: #161b22;
  border-bottom: 1px solid #21262d;
  flex-wrap: wrap; gap: 8px;
}

.toolbar-left, .toolbar-right { display: flex; align-items: center; gap: 8px; }

.search-input, .level-filter, .task-filter {
  background: #21262d; color: #e6edf3; border: 1px solid #30363d;
  border-radius: 6px; padding: 4px 10px; font-size: 12px; outline: none;
}
.search-input { width: 200px; }
.search-input::placeholder { color: #6e7681; }
.search-input:focus, .level-filter:focus { border-color: #388bfd; }

.log-count { font-size: 11px; color: #6e7681; font-family: monospace; }

.auto-scroll-toggle {
  display: flex; align-items: center; gap: 5px;
  font-size: 11px; color: #8b949e; cursor: pointer;
}

.btn-copy, .btn-clear {
  padding: 3px 10px; font-size: 11px;
  border-radius: 5px; cursor: pointer;
  background: #21262d; color: #8b949e; border: 1px solid #30363d;
}
.btn-copy:hover, .btn-clear:hover { color: #e6edf3; border-color: #8b949e; }

.log-container {
  flex: 1; overflow-y: auto;
  padding: 4px 0;
  scroll-behavior: smooth;
}

.log-container::-webkit-scrollbar { width: 6px; }
.log-container::-webkit-scrollbar-track { background: transparent; }
.log-container::-webkit-scrollbar-thumb { background: #30363d; border-radius: 3px; }

.empty-state {
  display: flex; flex-direction: column; align-items: center;
  justify-content: center; height: 200px; gap: 8px;
}
.empty-icon { font-size: 32px; opacity: 0.4; }
.empty-text { color: #6e7681; font-size: 13px; }

.log-entry {
  display: flex; align-items: baseline; gap: 8px;
  padding: 2px 12px; font-family: 'Fira Code', 'Cascadia Code', monospace;
  font-size: 11.5px; line-height: 1.7;
  transition: background 0.15s;
}
.log-entry:hover { background: rgba(255,255,255,0.03); }
.log-entry.new { animation: flashNew 0.4s ease; }

@keyframes flashNew {
  from { background: rgba(56,139,253,0.15); }
  to { background: transparent; }
}

.log-ts { color: #6e7681; flex-shrink: 0; font-size: 10px; }

.log-level-badge {
  flex-shrink: 0;
  font-size: 9px; font-weight: 700;
  width: 36px; text-align: center;
  border-radius: 3px; padding: 0 2px;
}
.log-level-badge.INFO { background: rgba(56,139,253,0.15); color: #79c0ff; }
.log-level-badge.WARN { background: rgba(210,153,34,0.15); color: #e3b341; }
.log-level-badge.ERROR { background: rgba(248,81,73,0.15); color: #ff7b72; }
.log-level-badge.DEBUG { background: rgba(110,118,129,0.15); color: #8b949e; }

.log-task {
  flex-shrink: 0;
  font-size: 10px; color: #a371f7;
  background: rgba(163,113,247,0.1);
  padding: 0 5px; border-radius: 3px;
}

.log-msg { color: #e6edf3; word-break: break-word; flex: 1; }
.log-entry.WARN .log-msg { color: #e3b341; }
.log-entry.ERROR .log-msg { color: #ff7b72; }

.log-fields { font-size: 10px; color: #6e7681; word-break: break-all; }

:deep(.highlight) {
  background: rgba(210,153,34,0.3);
  color: #e3b341;
  border-radius: 2px;
  padding: 0 1px;
}
</style>
