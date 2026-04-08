<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="brand">
        <div class="brand-mark">
          ⬡
        </div>
        <span class="brand-name">Fluxor</span>
      </div>
      <nav class="topbar-nav">
        <RouterLink
          v-for="r in navRoutes"
          :key="r.name as string"
          :to="r.path"
          class="nav-link"
          active-class="nav-link--active"
        >
          <span class="nav-icon">{{ r.meta.icon }}</span>{{ r.meta.title }}
        </RouterLink>
      </nav>
      <div class="topbar-end">
        <div
          class="ws-pill"
          :class="wsStore.status"
        >
          <span class="ws-dot"></span><span class="ws-label">{{ wsLabel }}</span>
        </div>
        <div class="metric-pill blue">
          <strong>{{ activeCount }}</strong><span>active</span>
        </div>
        <div class="metric-pill green">
          <strong>{{ wfStore.metrics?.workflows_completed ?? 0 }}</strong><span>done</span>
        </div>
        <div
          v-if="failedCount > 0"
          class="metric-pill red"
        >
          <strong>{{ failedCount }}</strong><span>failed</span>
        </div>
      </div>
    </header>
    <main class="page-content">
      <RouterView v-slot="{ Component }">
        <Transition
          name="fade"
          mode="out-in"
        >
          <component :is="Component" />
        </Transition>
      </RouterView>
    </main>
    <Teleport to="body">
      <Transition name="toast">
        <div
          v-if="toast"
          class="toast"
          :class="toast.type"
        >
          {{ toast.message }}
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, provide } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import { useWebSocketStore } from './stores/websocket'
import { useWorkflowStore } from './stores/workflow'
import { navRoutes } from './router'

const wsStore = useWebSocketStore()
const wfStore = useWorkflowStore()

const activeCount = computed(
  () => wfStore.executions.filter((e) => e.status === 'running' || e.status === 'pending').length,
)
const failedCount = computed(() => wfStore.metrics?.workflows_failed ?? 0)
const wsLabel = computed(() => {
  switch (wsStore.status) {
    case 'connected':
      return `Live`
    case 'connecting':
      return 'Connecting…'
    case 'error':
      return 'Error'
    default:
      return `Retry ${wsStore.reconnectAttempts}`
  }
})

interface Toast {
  message: string
  type: 'success' | 'error' | 'info'
}
const toast = ref<Toast | null>(null)
let toastTimer = 0
function showToast(message: string, type: Toast['type'] = 'info') {
  clearTimeout(toastTimer)
  toast.value = { message, type }
  toastTimer = window.setTimeout(() => {
    toast.value = null
  }, 3500)
}
provide('showToast', showToast)

let metricsTimer = 0
onMounted(async () => {
  wsStore.connect()
  await Promise.all([wfStore.fetchDefinitions(), wfStore.fetchExecutions(), wfStore.fetchMetrics()])
  metricsTimer = window.setInterval(() => wfStore.fetchMetrics(), 5000)
})
onUnmounted(() => {
  wsStore.disconnect()
  clearInterval(metricsTimer)
})
</script>

<style scoped>
.app-shell {
  display: flex;
  flex-direction: column;
  height: 100vh;
  overflow: hidden;
}
.topbar {
  height: 50px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  padding: 0 16px;
  gap: 16px;
  background: var(--bg2);
  border-bottom: 1px solid var(--border);
}
.brand {
  display: flex;
  align-items: center;
  gap: 8px;
}
.brand-mark {
  width: 26px;
  height: 26px;
  border-radius: 7px;
  background: linear-gradient(135deg, #7c6aff, #3b9eff);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
}
.brand-name {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: -0.02em;
}
.topbar-nav {
  display: flex;
  gap: 2px;
  margin-left: 8px;
}
.topbar-end {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-left: auto;
}
.nav-link {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 5px 12px;
  border-radius: var(--r-sm);
  font-size: 13px;
  font-weight: 500;
  color: var(--text2);
  text-decoration: none;
  transition: all 0.12s;
}
.nav-link:hover {
  background: var(--bg3);
  color: var(--text);
}
.nav-link--active {
  background: var(--surface);
  color: var(--text);
}
.nav-icon {
  font-size: 11px;
}
.ws-pill {
  display: flex;
  align-items: center;
  gap: 6px;
}
.ws-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text3);
}
.ws-pill.connected .ws-dot {
  background: var(--green);
  animation: wsPulse 2s infinite;
}
.ws-pill.connecting .ws-dot {
  background: var(--amber);
  animation: wsPulse 1s infinite;
}
.ws-pill.error .ws-dot {
  background: var(--red);
}
@keyframes wsPulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}
.ws-label {
  font-size: 11px;
  color: var(--text3);
  font-family: var(--mono);
}
.metric-pill {
  display: flex;
  align-items: baseline;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 20px;
  border: 1px solid;
  font-size: 11px;
}
.metric-pill strong {
  font-size: 13px;
  font-weight: 700;
  font-family: var(--mono);
}
.metric-pill.blue {
  background: rgba(59, 158, 255, 0.08);
  border-color: rgba(59, 158, 255, 0.2);
  color: var(--blue);
}
.metric-pill.green {
  background: rgba(34, 211, 160, 0.08);
  border-color: rgba(34, 211, 160, 0.2);
  color: var(--green);
}
.metric-pill.red {
  background: rgba(255, 95, 87, 0.08);
  border-color: rgba(255, 95, 87, 0.2);
  color: var(--red);
}
.page-content {
  flex: 1;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.15s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
.toast {
  position: fixed;
  bottom: 20px;
  right: 20px;
  z-index: 9999;
  padding: 10px 18px;
  border-radius: var(--r);
  font-size: 12px;
  font-weight: 500;
  border: 1px solid;
}
.toast.success {
  background: rgba(34, 211, 160, 0.12);
  border-color: rgba(34, 211, 160, 0.3);
  color: var(--green);
}
.toast.error {
  background: rgba(255, 95, 87, 0.12);
  border-color: rgba(255, 95, 87, 0.3);
  color: var(--red);
}
.toast.info {
  background: rgba(59, 158, 255, 0.12);
  border-color: rgba(59, 158, 255, 0.3);
  color: var(--blue);
}
.toast-enter-active {
  animation: toastIn 0.2s ease;
}
.toast-leave-active {
  animation: toastIn 0.2s ease reverse;
}
@keyframes toastIn {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
