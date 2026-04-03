<template>
  <Transition name="drawer">
    <div v-if="task" class="drawer-backdrop" @click.self="$emit('close')">
      <div class="drawer">
        <!-- Header -->
        <div class="drawer-header">
          <div class="drawer-title-row">
            <StatusBadge :status="task.status" />
            <h2 class="drawer-title">{{ task.task_name }}</h2>
          </div>
          <button class="drawer-close" @click="$emit('close')">✕</button>
        </div>

        <!-- Meta grid -->
        <div class="meta-grid">
          <div class="meta-item">
            <span class="meta-key">Type</span>
            <span class="meta-val">{{ typeLabel }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-key">Worker</span>
            <span class="meta-val mono">{{ task.worker_id || '—' }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-key">Retries</span>
            <span class="meta-val" :class="{ 'warn-val': task.retry_count > 0 }">
              {{ task.retry_count }} / {{ task.max_retries }}
            </span>
          </div>
          <div class="meta-item">
            <span class="meta-key">Duration</span>
            <span class="meta-val mono">{{ duration }}</span>
          </div>
          <div class="meta-item" v-if="task.queued_at">
            <span class="meta-key">Queued at</span>
            <span class="meta-val mono">{{ fmt(task.queued_at) }}</span>
          </div>
          <div class="meta-item" v-if="task.started_at">
            <span class="meta-key">Started at</span>
            <span class="meta-val mono">{{ fmt(task.started_at) }}</span>
          </div>
          <div class="meta-item" v-if="task.completed_at">
            <span class="meta-key">Completed at</span>
            <span class="meta-val mono">{{ fmt(task.completed_at) }}</span>
          </div>
          <div class="meta-item" v-if="task.next_retry_at">
            <span class="meta-key">Next retry</span>
            <span class="meta-val mono amber-val">{{ fmt(task.next_retry_at) }}</span>
          </div>
        </div>

        <!-- Error block -->
        <div v-if="task.error" class="error-block">
          <div class="error-label">Error</div>
          <pre class="error-text">{{ task.error }}</pre>
        </div>

        <!-- Output block -->
        <div v-if="task.output" class="output-block">
          <div class="output-label">
            Output
            <button class="copy-btn" @click="copyOutput">Copy</button>
          </div>
          <pre class="output-text">{{ formattedOutput }}</pre>
        </div>

        <!-- Tabs: Logs / Config -->
        <div class="drawer-tabs">
          <button
            v-for="tab in ['logs', 'config']"
            :key="tab"
            class="drawer-tab"
            :class="{ active: activeTab === tab }"
            @click="activeTab = tab as any"
          >{{ tab }}</button>
        </div>

        <!-- Logs tab -->
        <div v-if="activeTab === 'logs'" class="drawer-logs">
          <div class="logs-toolbar">
            <input v-model="logSearch" placeholder="Filter logs…" class="log-filter-input" />
            <span class="log-count-badge">{{ filteredLogs.length }} entries</span>
            <label class="auto-scroll-label">
              <input type="checkbox" v-model="autoScroll" /> Auto-scroll
            </label>
          </div>
          <div class="log-list" ref="logListEl">
            <div v-if="!filteredLogs.length" class="log-empty">
              {{ task.logs?.length ? 'No matching logs' : 'No logs available' }}
            </div>
            <div
              v-for="(entry, i) in filteredLogs"
              :key="i"
              class="log-row"
              :class="entry.level"
            >
              <span class="log-ts">{{ fmtMs(entry.timestamp) }}</span>
              <span class="log-lvl" :class="entry.level">{{ entry.level?.toUpperCase() }}</span>
              <span class="log-msg">{{ entry.message }}</span>
              <span v-if="hasFields(entry)" class="log-fields">{{ JSON.stringify(entry.fields) }}</span>
            </div>
          </div>
        </div>

        <!-- Config tab -->
        <div v-if="activeTab === 'config'" class="drawer-config">
          <div class="config-item">
            <span class="meta-key">Task definition ID</span>
            <span class="meta-val mono">{{ task.task_definition_id }}</span>
          </div>
          <div class="config-item">
            <span class="meta-key">Workflow execution ID</span>
            <span class="meta-val mono">{{ task.workflow_exec_id }}</span>
          </div>
          <div class="config-item">
            <span class="meta-key">Task execution ID</span>
            <span class="meta-val mono">{{ task.id }}</span>
          </div>
        </div>
      </div>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { ref, computed, watch, nextTick } from 'vue'
import type { TaskExecution } from '../../types'
import { TASK_TYPES } from '../../types'
import StatusBadge from './StatusBadge.vue'

const props = defineProps<{ task: TaskExecution | null }>()
defineEmits<{ (e: 'close'): void }>()

const activeTab = ref<'logs' | 'config'>('logs')
const logSearch = ref('')
const autoScroll = ref(true)
const logListEl = ref<HTMLElement | null>(null)

const typeLabel = computed(() =>
  TASK_TYPES.find(t => t.value === props.task?.task_type)?.label ?? props.task?.task_type ?? '—'
)

const duration = computed(() => {
  const t = props.task
  if (!t?.started_at) return '—'
  const ms = (t.completed_at ? new Date(t.completed_at) : new Date()).getTime()
           - new Date(t.started_at).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
})

const filteredLogs = computed(() => {
  const logs = props.task?.logs ?? []
  if (!logSearch.value) return logs
  const q = logSearch.value.toLowerCase()
  return logs.filter(l =>
    l.message?.toLowerCase().includes(q) ||
    JSON.stringify(l.fields ?? {}).toLowerCase().includes(q)
  )
})

const formattedOutput = computed(() => {
  try { return JSON.stringify(props.task?.output, null, 2) }
  catch { return String(props.task?.output) }
})

watch(filteredLogs, async () => {
  if (!autoScroll.value) return
  await nextTick()
  if (logListEl.value) logListEl.value.scrollTop = logListEl.value.scrollHeight
})

watch(() => props.task?.id, () => { activeTab.value = 'logs'; logSearch.value = '' })

function fmt(ts: string): string {
  return new Date(ts).toLocaleTimeString('en', { hour12: false })
}

function fmtMs(ts: string): string {
  const d = new Date(ts)
  return `${d.toLocaleTimeString('en', { hour12: false })}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

function hasFields(entry: { fields?: Record<string, unknown> }): boolean {
  return !!entry.fields && Object.keys(entry.fields).length > 0
}

function copyOutput() {
  navigator.clipboard.writeText(formattedOutput.value)
}
</script>

<style scoped>
.drawer-backdrop {
  position: fixed; inset: 0; z-index: 200;
  background: rgba(0,0,0,.5); backdrop-filter: blur(2px);
  display: flex; justify-content: flex-end;
}

.drawer {
  width: 480px; max-width: 95vw; height: 100%;
  background: var(--bg2);
  border-left: 1px solid var(--border2);
  display: flex; flex-direction: column;
  overflow: hidden;
}

.drawer-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 16px; border-bottom: 1px solid var(--border);
  flex-shrink: 0; gap: 10px;
}
.drawer-title-row { display: flex; align-items: center; gap: 10px; }
.drawer-title { font-size: 14px; font-weight: 600; }
.drawer-close { background: none; border: none; color: var(--text3); font-size: 16px; cursor: pointer; }
.drawer-close:hover { color: var(--text); }

/* Meta grid */
.meta-grid {
  display: grid; grid-template-columns: 1fr 1fr;
  gap: 1px; background: var(--border);
  border-bottom: 1px solid var(--border); flex-shrink: 0;
}
.meta-item {
  display: flex; flex-direction: column; gap: 3px;
  padding: 10px 14px; background: var(--bg2);
}
.meta-key { font-size: 9px; font-weight: 600; text-transform: uppercase; letter-spacing: .08em; color: var(--text3); }
.meta-val { font-size: 12px; color: var(--text2); }
.meta-val.mono { font-family: var(--mono); font-size: 11px; }
.warn-val { color: var(--amber); }
.amber-val { color: var(--amber); }

/* Error */
.error-block { padding: 12px 16px; border-bottom: 1px solid var(--border); flex-shrink: 0; }
.error-label { font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .08em; color: var(--red); margin-bottom: 6px; }
.error-text { font-size: 11px; font-family: var(--mono); color: var(--red); background: rgba(255,95,87,.08); border: 1px solid rgba(255,95,87,.2); border-radius: var(--radius-sm); padding: 8px 10px; white-space: pre-wrap; word-break: break-all; margin: 0; }

/* Output */
.output-block { padding: 12px 16px; border-bottom: 1px solid var(--border); flex-shrink: 0; max-height: 150px; overflow: hidden; }
.output-label { display: flex; align-items: center; justify-content: space-between; font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .08em; color: var(--text3); margin-bottom: 6px; }
.copy-btn { background: none; border: none; color: var(--text3); font-size: 10px; cursor: pointer; padding: 0; }
.copy-btn:hover { color: var(--text2); }
.output-text { font-size: 10px; font-family: var(--mono); color: var(--green); background: rgba(34,211,160,.05); border: 1px solid rgba(34,211,160,.1); border-radius: var(--radius-sm); padding: 8px 10px; white-space: pre-wrap; word-break: break-all; margin: 0; max-height: 100px; overflow-y: auto; }

/* Tabs */
.drawer-tabs { display: flex; border-bottom: 1px solid var(--border); flex-shrink: 0; }
.drawer-tab { padding: 8px 16px; background: none; border: none; border-bottom: 2px solid transparent; color: var(--text3); font-size: 12px; font-weight: 500; cursor: pointer; text-transform: capitalize; }
.drawer-tab.active { color: var(--text); border-bottom-color: var(--accent); }
.drawer-tab:hover:not(.active) { color: var(--text2); }

/* Logs */
.drawer-logs { flex: 1; display: flex; flex-direction: column; overflow: hidden; }
.logs-toolbar { display: flex; align-items: center; gap: 8px; padding: 8px 14px; border-bottom: 1px solid var(--border); flex-shrink: 0; }
.log-filter-input { flex: 1; background: var(--surface); border: 1px solid var(--border2); border-radius: var(--radius-sm); color: var(--text); font-size: 11px; padding: 4px 8px; outline: none; font-family: var(--mono); }
.log-filter-input:focus { border-color: var(--accent); }
.log-count-badge { font-size: 10px; color: var(--text3); font-family: var(--mono); flex-shrink: 0; }
.auto-scroll-label { font-size: 10px; color: var(--text3); cursor: pointer; display: flex; align-items: center; gap: 4px; flex-shrink: 0; }

.log-list { flex: 1; overflow-y: auto; background: #090910; }
.log-list::-webkit-scrollbar { width: 4px; }
.log-list::-webkit-scrollbar-thumb { background: var(--border2); }

.log-empty { padding: 24px; text-align: center; color: var(--text3); font-size: 12px; }
.log-row { display: flex; align-items: baseline; gap: 7px; padding: 1.5px 14px; font-family: var(--mono); font-size: 10.5px; line-height: 1.7; }
.log-row:hover { background: rgba(255,255,255,.03); }
.log-ts { color: #4a5568; flex-shrink: 0; }
.log-lvl { width: 36px; text-align: center; font-size: 9px; font-weight: 700; border-radius: 3px; padding: 0 2px; flex-shrink: 0; }
.log-lvl.info  { background: rgba(59,158,255,.12); color: #63b3ed; }
.log-lvl.warn  { background: rgba(245,166,35,.12); color: #ecc94b; }
.log-lvl.error { background: rgba(255,95,87,.12); color: #fc8181; }
.log-lvl.debug { background: rgba(110,118,129,.12); color: #8b949e; }
.log-msg { color: #cbd5e0; flex: 1; }
.log-row.warn .log-msg { color: #ecc94b; }
.log-row.error .log-msg { color: #fc8181; }
.log-fields { font-size: 9.5px; color: #4a5568; word-break: break-all; }

/* Config */
.drawer-config { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.config-item { display: flex; flex-direction: column; gap: 4px; padding: 8px 10px; background: var(--surface); border-radius: var(--radius-sm); }

/* Transition */
.drawer-enter-active, .drawer-leave-active { transition: transform .25s ease; }
.drawer-enter-from .drawer, .drawer-leave-to .drawer { transform: translateX(100%); }
.drawer-enter-active .drawer-backdrop, .drawer-leave-active .drawer-backdrop { transition: opacity .25s; }
.drawer-enter-from, .drawer-leave-to { opacity: 0; }
</style>
