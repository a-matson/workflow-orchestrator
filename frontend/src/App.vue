<template>
  <div class="app">
    <header class="topbar">
      <div class="topbar-brand" @click="sidebarCollapsed = !sidebarCollapsed">
        <div class="brand-mark">⬡</div>
        <span class="brand-name">Fluxor</span>
      </div>
      <nav class="topbar-nav">
        <button v-for="tab in TABS" :key="tab.id" class="nav-tab" :class="{ active: activeTab === tab.id }" @click="activeTab = tab.id">
          <span class="tab-icon">{{ tab.icon }}</span>{{ tab.label }}
        </button>
      </nav>
      <div class="topbar-end">
        <WorkflowSearch @navigate-execution="onSearchExecution" @navigate-workflow="onSearchWorkflow" />
        <div class="ws-status" :class="wsStore.status">
          <div class="ws-dot"></div>
          <span class="ws-label">{{ wsLabel }}</span>
        </div>
        <div class="topbar-pills">
          <div class="pill blue"><span class="pill-val">{{ activeCount }}</span><span class="pill-key">active</span></div>
          <div class="pill green"><span class="pill-val">{{ metrics?.workflows_completed ?? 0 }}</span><span class="pill-key">done</span></div>
          <div class="pill red" v-if="(metrics?.workflows_failed ?? 0) > 0"><span class="pill-val">{{ metrics?.workflows_failed }}</span><span class="pill-key">failed</span></div>
          <div class="pill amber"><span class="pill-val">{{ metrics?.queue_depth ?? 0 }}</span><span class="pill-key">queued</span></div>
        </div>
      </div>
    </header>

    <div class="app-body">
      <Transition name="sidebar">
        <aside class="sidebar" v-show="!sidebarCollapsed">
          <div class="sidebar-search">
            <input v-model="wfSearch" placeholder="Filter workflows…" class="search-input" />
          </div>
          <div class="sidebar-section-label">Workflows</div>
          <div class="sidebar-list">
            <div v-for="wf in filteredWorkflows" :key="wf.id" class="sidebar-item" :class="{ active: selectedWorkflowId === wf.id }" @click="selectWorkflow(wf.id)">
              <div class="sidebar-item-icon">◈</div>
              <div class="sidebar-item-body">
                <div class="sidebar-item-name">{{ wf.name }}</div>
                <div class="sidebar-item-sub">{{ wf.tasks.length }} tasks · v{{ wf.version }}</div>
              </div>
              <button class="sidebar-run-btn" @click.stop="runWorkflow(wf.id)">▶</button>
            </div>
            <div v-if="!filteredWorkflows.length" class="sidebar-empty">{{ wfSearch ? "No matches" : "No workflows yet" }}</div>
          </div>
          <div class="sidebar-section-label" style="margin-top:8px">Recent Runs</div>
          <div class="sidebar-list">
            <div v-for="exec in recentRuns" :key="exec.id" class="sidebar-item" :class="{ active: selectedExecId === exec.id }" @click="selectExecution(exec.id)">
              <span class="badge" :class="exec.status" style="flex-shrink:0">{{ exec.status }}</span>
              <div class="sidebar-item-body">
                <div class="sidebar-item-name">{{ exec.workflow_name }}</div>
                <div class="sidebar-item-sub">{{ relativeTime(exec.created_at) }}</div>
              </div>
            </div>
          </div>
          <div class="sidebar-footer">
            <button class="new-wf-btn" @click="showTemplates = true; activeTab = 'builder'">+ From Template</button>
          </div>
        </aside>
      </Transition>

      <main class="content">
        <div class="panel" :class="{ active: activeTab === 'builder' }">
          <div class="builder-layout">
            <Transition name="slide-left">
              <WorkflowTemplates v-if="showTemplates" class="templates-sidebar" @select="loadTemplate" @close="showTemplates = false" />
            </Transition>
            <DAGEditor ref="dagEditorRef" class="dag-editor-main" @triggered="onTriggered" />
          </div>
        </div>
        <div class="panel" :class="{ active: activeTab === 'executions' }">
          <ExecutionsView :executions="store.executions" :selected-id="selectedExecId" @select="selectExecution" @retry="retryExecution" />
        </div>
        <div class="panel" :class="{ active: activeTab === 'logs' }">
          <div v-if="selectedExecution" class="logs-layout">
            <LogViewer :execution="selectedExecution" />
            <ExecutionTimeline :execution="selectedExecution" class="timeline-panel" />
          </div>
          <div v-else class="empty-panel">
            <div class="empty-icon">≡</div>
            <div class="empty-text">Select an execution to view logs and timeline</div>
            <button class="empty-cta" @click="activeTab = 'executions'">Go to Executions →</button>
          </div>
        </div>
        <div class="panel" :class="{ active: activeTab === 'metrics' }">
          <MetricsView :metrics="metrics" :executions="store.executions" />
        </div>
      </main>
    </div>

    <Transition name="toast">
      <div v-if="toast" class="toast" :class="toast.type">{{ toast.message }}</div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useWorkflowStore } from './stores/workflow'
import { useWebSocketStore } from './stores/websocket'
import { WS_URL } from './composables/useApi'
import DAGEditor from './components/dag/DAGEditor.vue'
import WorkflowTemplates from './components/dag/WorkflowTemplates.vue'
import LogViewer from './components/logs/LogViewer.vue'
import ExecutionsView from './components/execution/ExecutionsView.vue'
import ExecutionTimeline from './components/execution/ExecutionTimeline.vue'
import MetricsView from './components/metrics/MetricsView.vue'
import WorkflowSearch from './components/shared/WorkflowSearch.vue'
import type { WorkflowDefinition } from './types'

const TABS = [
  { id: 'builder',    label: 'Builder',    icon: '◈' },
  { id: 'executions', label: 'Executions', icon: '⬡' },
  { id: 'logs',       label: 'Logs',       icon: '≡' },
  { id: 'metrics',    label: 'Metrics',    icon: '◎' },
] as const
type TabId = typeof TABS[number]['id']

const activeTab = ref<TabId>('builder')
const sidebarCollapsed = ref(false)
const showTemplates = ref(false)
const wfSearch = ref('')
const dagEditorRef = ref<InstanceType<typeof DAGEditor> | null>(null)
const selectedWorkflowId = ref<string | null>(null)
const selectedExecId = ref<string | null>(null)
const store = useWorkflowStore()
const wsStore = useWebSocketStore()

const metrics = computed(() => store.metrics)
const filteredWorkflows = computed(() => store.definitions.filter(w => !wfSearch.value || w.name.toLowerCase().includes(wfSearch.value.toLowerCase())))
const recentRuns = computed(() => store.executions.slice(0, 8))
const selectedExecution = computed(() => store.executions.find(e => e.id === selectedExecId.value) ?? store.selectedExecution)
const activeCount = computed(() => store.executions.filter(e => e.status === 'running' || e.status === 'pending').length)
const wsLabel = computed(() => {
  switch (wsStore.status) {
    case 'connected': return `Live · ${metrics.value?.ws_clients ?? 1} clients`
    case 'connecting': return 'Connecting…'
    case 'disconnected': return `Reconnect ${wsStore.reconnectAttempts}`
    default: return 'Error'
  }
})

const toast = ref<{ message: string; type: 'success' | 'error' | 'info' } | null>(null)
let toastTimer: ReturnType<typeof setTimeout>
function showToast(message: string, type: 'success' | 'error' | 'info' = 'info') {
  clearTimeout(toastTimer)
  toast.value = { message, type }
  toastTimer = setTimeout(() => { toast.value = null }, 3500)
}

function selectWorkflow(id: string) {
  selectedWorkflowId.value = id
  const wf = store.definitions.find(w => w.id === id)
  if (wf && dagEditorRef.value) { dagEditorRef.value.loadWorkflow(wf.tasks); activeTab.value = 'builder'; showTemplates.value = false }
}
async function runWorkflow(id: string) {
  try { const exec = await store.triggerWorkflow(id, {}); selectedExecId.value = exec.id; wsStore.subscribe(exec.id); activeTab.value = 'executions'; showToast(`▶ Started: ${exec.workflow_name}`, 'success') }
  catch { showToast('Failed to start workflow', 'error') }
}
function onTriggered(execId: string) { selectedExecId.value = execId; wsStore.subscribe(execId); activeTab.value = 'executions'; showToast('▶ Workflow triggered', 'success') }
function selectExecution(id: string) { selectedExecId.value = id; store.fetchExecution(id); wsStore.subscribe(id) }
async function retryExecution(id: string) {
  try { const e = await store.retryExecution(id); selectedExecId.value = e.id; wsStore.subscribe(e.id); showToast('↺ Re-queued', 'info') }
  catch { showToast('Retry failed', 'error') }
}
function loadTemplate(tpl: Omit<WorkflowDefinition, 'id' | 'created_at' | 'updated_at'>) { dagEditorRef.value?.loadWorkflow(tpl.tasks as any); showTemplates.value = false; activeTab.value = 'builder' }
function onSearchExecution(id: string) { selectedExecId.value = id; activeTab.value = 'executions' }
function onSearchWorkflow(id: string) { selectWorkflow(id) }
function relativeTime(ts: string): string { const ms = Date.now() - new Date(ts).getTime(); if (ms < 60000) return `${Math.floor(ms/1000)}s ago`; if (ms < 3600000) return `${Math.floor(ms/60000)}m ago`; return `${Math.floor(ms/3600000)}h ago` }

let metricsTimer: ReturnType<typeof setInterval>
onMounted(async () => { wsStore.connect(WS_URL); await Promise.all([store.fetchDefinitions(), store.fetchExecutions(), store.fetchMetrics()]); metricsTimer = setInterval(() => store.fetchMetrics(), 5000) })
onUnmounted(() => { wsStore.disconnect(); clearInterval(metricsTimer) })
</script>

<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
:root{--bg:#0c0c14;--bg2:#13131f;--bg3:#1a1a2a;--surface:#1e1e30;--surface2:#252538;--border:rgba(255,255,255,0.07);--border2:rgba(255,255,255,0.13);--text:#eaeaf5;--text2:#9494b0;--text3:#58587a;--accent:#7c6aff;--accent2:#5b4bd4;--green:#22d3a0;--amber:#f5a623;--red:#ff5f57;--blue:#3b9eff;--purple:#c084fc;--mono:"IBM Plex Mono",monospace;--sans:"DM Sans",system-ui,sans-serif;--radius-sm:6px;--radius:9px;--radius-lg:13px}
html,body,#app{height:100%;overflow:hidden}
body{font-family:var(--sans);background:var(--bg);color:var(--text);-webkit-font-smoothing:antialiased}
button{font-family:var(--sans);cursor:pointer}
.app{display:flex;flex-direction:column;height:100vh;overflow:hidden}
.app-body{display:flex;flex:1;overflow:hidden}
.topbar{height:50px;flex-shrink:0;display:flex;align-items:center;padding:0 16px;gap:14px;background:var(--bg2);border-bottom:1px solid var(--border)}
.topbar-brand{display:flex;align-items:center;gap:8px;cursor:pointer;user-select:none}
.brand-mark{width:26px;height:26px;border-radius:7px;background:linear-gradient(135deg,#7c6aff,#3b9eff);display:flex;align-items:center;justify-content:center;font-size:13px}
.brand-name{font-size:15px;font-weight:700;letter-spacing:-.02em}
.topbar-nav{display:flex;gap:2px}
.nav-tab{display:flex;align-items:center;gap:5px;padding:5px 12px;border-radius:var(--radius-sm);font-size:13px;font-weight:500;background:none;border:none;color:var(--text2);transition:all .12s}
.nav-tab:hover{background:var(--bg3);color:var(--text)}
.nav-tab.active{background:var(--surface);color:var(--text)}
.tab-icon{font-size:11px}
.topbar-end{display:flex;align-items:center;gap:12px;margin-left:auto}
.ws-status{display:flex;align-items:center;gap:6px}
.ws-dot{width:6px;height:6px;border-radius:50%;background:var(--text3)}
.ws-status.connected .ws-dot{background:var(--green);animation:wsPulse 2s infinite}
.ws-status.connecting .ws-dot{background:var(--amber);animation:wsPulse 1s infinite}
.ws-status.error .ws-dot{background:var(--red)}
@keyframes wsPulse{0%,100%{opacity:1}50%{opacity:.4}}
.ws-label{font-size:11px;color:var(--text3);font-family:var(--mono)}
.topbar-pills{display:flex;gap:7px}
.pill{display:flex;align-items:baseline;gap:4px;padding:2px 8px;border-radius:20px;border:1px solid}
.pill-val{font-size:13px;font-weight:700;font-family:var(--mono)}
.pill-key{font-size:9px;text-transform:uppercase;letter-spacing:.06em}
.pill.blue{background:rgba(59,158,255,.08);border-color:rgba(59,158,255,.2);color:var(--blue)}
.pill.green{background:rgba(34,211,160,.08);border-color:rgba(34,211,160,.2);color:var(--green)}
.pill.red{background:rgba(255,95,87,.08);border-color:rgba(255,95,87,.2);color:var(--red)}
.pill.amber{background:rgba(245,166,35,.08);border-color:rgba(245,166,35,.2);color:var(--amber)}
.sidebar{width:240px;flex-shrink:0;background:var(--bg2);border-right:1px solid var(--border);display:flex;flex-direction:column;overflow:hidden}
.sidebar-search{padding:10px 12px}
.search-input{width:100%;padding:6px 10px;background:var(--surface);border:1px solid var(--border2);border-radius:var(--radius-sm);color:var(--text);font-size:12px;outline:none}
.search-input:focus{border-color:var(--accent)}
.search-input::placeholder{color:var(--text3)}
.sidebar-section-label{padding:0 12px 5px;font-size:9px;font-weight:700;text-transform:uppercase;letter-spacing:.1em;color:var(--text3)}
.sidebar-list{flex:1;overflow-y:auto;padding-bottom:4px}
.sidebar-list::-webkit-scrollbar{width:3px}
.sidebar-list::-webkit-scrollbar-thumb{background:var(--border2)}
.sidebar-item{display:flex;align-items:center;gap:8px;padding:6px 12px;cursor:pointer;transition:background .1s;border-left:2px solid transparent}
.sidebar-item:hover{background:var(--bg3)}
.sidebar-item.active{background:rgba(124,106,255,.1);border-left-color:var(--accent)}
.sidebar-item-icon{font-size:13px;flex-shrink:0;color:var(--text3)}
.sidebar-item.active .sidebar-item-icon{color:var(--accent)}
.sidebar-item-body{flex:1;min-width:0}
.sidebar-item-name{font-size:12px;font-weight:500;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.sidebar-item-sub{font-size:10px;color:var(--text3)}
.sidebar-run-btn{width:20px;height:20px;border-radius:4px;background:rgba(34,211,160,.12);border:none;color:var(--green);font-size:8px;opacity:0;transition:opacity .15s;display:flex;align-items:center;justify-content:center;flex-shrink:0}
.sidebar-item:hover .sidebar-run-btn{opacity:1}
.sidebar-empty{padding:16px 12px;font-size:11px;color:var(--text3);text-align:center}
.sidebar-footer{padding:10px 12px;border-top:1px solid var(--border)}
.new-wf-btn{width:100%;padding:7px;background:rgba(124,106,255,.1);border:1px dashed rgba(124,106,255,.3);border-radius:var(--radius);color:var(--accent);font-size:12px;font-weight:500}
.new-wf-btn:hover{background:rgba(124,106,255,.2)}
.badge{display:inline-flex;align-items:center;gap:4px;padding:2px 8px;border-radius:20px;font-size:10px;font-weight:600;white-space:nowrap}
.badge::before{content:"";width:5px;height:5px;border-radius:50%;background:currentColor}
.badge.pending{background:rgba(144,144,168,.1);color:#9090a8}
.badge.queued{background:rgba(192,132,252,.1);color:var(--purple)}
.badge.running{background:rgba(59,158,255,.1);color:var(--blue);animation:badgePulse 1.5s infinite}
.badge.completed{background:rgba(34,211,160,.1);color:var(--green)}
.badge.failed{background:rgba(255,95,87,.1);color:var(--red)}
.badge.retrying{background:rgba(245,166,35,.1);color:var(--amber)}
.badge.dead_letter{background:rgba(197,48,48,.08);color:#c53030}
@keyframes badgePulse{0%,100%{opacity:1}50%{opacity:.65}}
.content{flex:1;overflow:hidden;position:relative}
.panel{display:none;position:absolute;inset:0;flex-direction:column;overflow:hidden}
.panel.active{display:flex}
.builder-layout{display:flex;height:100%;overflow:hidden}
.templates-sidebar{width:260px;flex-shrink:0}
.dag-editor-main{flex:1;overflow:hidden}
.logs-layout{display:flex;flex-direction:column;height:100%;overflow:hidden}
.timeline-panel{flex-shrink:0;border-top:1px solid var(--border);max-height:260px;position:relative;overflow:hidden}
.empty-panel{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:10px;color:var(--text3)}
.empty-icon{font-size:36px;opacity:.25}
.empty-text{font-size:13px}
.empty-cta{margin-top:4px;padding:7px 16px;background:var(--surface);border:1px solid var(--border2);border-radius:var(--radius);color:var(--text2);font-size:12px;font-weight:500}
.empty-cta:hover{color:var(--text)}
.toast{position:fixed;bottom:20px;right:20px;z-index:9999;padding:10px 18px;border-radius:var(--radius);font-size:12px;font-weight:500;border:1px solid}
.toast.success{background:rgba(34,211,160,.12);border-color:rgba(34,211,160,.3);color:var(--green)}
.toast.error{background:rgba(255,95,87,.12);border-color:rgba(255,95,87,.3);color:var(--red)}
.toast.info{background:rgba(59,158,255,.12);border-color:rgba(59,158,255,.3);color:var(--blue)}
.toast-enter-active{animation:toastIn .2s ease}
.toast-leave-active{animation:toastIn .2s ease reverse}
@keyframes toastIn{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:translateY(0)}}
.sidebar-enter-active,.sidebar-leave-active{transition:width .2s ease,opacity .2s;overflow:hidden}
.sidebar-enter-from,.sidebar-leave-to{width:0;opacity:0}
.slide-left-enter-active,.slide-left-leave-active{transition:all .2s ease;overflow:hidden}
.slide-left-enter-from,.slide-left-leave-to{width:0;opacity:0}
</style>
