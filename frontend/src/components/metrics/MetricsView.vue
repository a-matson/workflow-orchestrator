<template>
  <div class="metrics-view">

    <!-- KPI cards row -->
    <div class="kpi-row">
      <div class="kpi-card" v-for="kpi in kpiCards" :key="kpi.label">
        <div class="kpi-val" :style="{ color: kpi.color }">{{ kpi.value }}</div>
        <div class="kpi-label">{{ kpi.label }}</div>
        <div v-if="kpi.sub" class="kpi-sub" :style="{ color: kpi.subColor }">{{ kpi.sub }}</div>
      </div>
    </div>

    <!-- Charts row -->
    <div class="charts-row">

      <!-- Throughput sparkline -->
      <div class="chart-card">
        <div class="chart-title">Task throughput</div>
        <div class="chart-subtitle">Tasks completed over time</div>
        <div class="sparkline-wrap">
          <svg :viewBox="`0 0 ${SPARK_W} ${SPARK_H}`" width="100%" height="80">
            <defs>
              <linearGradient id="spark-grad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="#7c6aff" stop-opacity="0.4"/>
                <stop offset="100%" stop-color="#7c6aff" stop-opacity="0"/>
              </linearGradient>
            </defs>
            <path :d="sparkAreaPath" fill="url(#spark-grad)" />
            <path :d="sparkLinePath" fill="none" stroke="#7c6aff" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
            <circle v-if="sparkPoints.length" :cx="sparkPoints[sparkPoints.length-1].x" :cy="sparkPoints[sparkPoints.length-1].y" r="3" fill="#7c6aff" />
          </svg>
          <div class="spark-labels">
            <span>{{ THROUGHPUT_WINDOW }}s ago</span>
            <span>now</span>
          </div>
        </div>
      </div>

      <!-- Status donut -->
      <div class="chart-card">
        <div class="chart-title">Task status breakdown</div>
        <div class="chart-subtitle">All-time across executions</div>
        <div class="donut-wrap">
          <svg viewBox="0 0 120 120" width="120" height="120">
            <g transform="translate(60,60)">
              <circle r="38" fill="none" stroke="var(--surface2)" stroke-width="16" />
              <path
                v-for="(seg, i) in donutSegments"
                :key="i"
                :d="seg.d"
                fill="none"
                :stroke="seg.color"
                stroke-width="16"
                stroke-linecap="butt"
              />
            </g>
            <text x="60" y="55" text-anchor="middle" font-size="18" font-weight="700" font-family="IBM Plex Mono,monospace" fill="var(--text)">{{ totalTasks }}</text>
            <text x="60" y="70" text-anchor="middle" font-size="9" fill="var(--text3)">tasks</text>
          </svg>
          <div class="donut-legend">
            <div v-for="seg in donutSegments" :key="seg.status" class="legend-row">
              <span class="legend-dot" :style="{ background: seg.color }"></span>
              <span class="legend-label">{{ seg.status }}</span>
              <span class="legend-val">{{ seg.count }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Queue depths -->
      <div class="chart-card">
        <div class="chart-title">Queue health</div>
        <div class="chart-subtitle">Real-time broker state</div>
        <div class="queue-stats">
          <div class="queue-stat">
            <div class="queue-stat-label">Task queue</div>
            <div class="queue-bar-wrap">
              <div class="queue-bar" :style="{ width: queueBarPct(metrics?.queue_depth ?? 0, 50) + '%', background: '#7c6aff' }"></div>
            </div>
            <div class="queue-stat-val">{{ metrics?.queue_depth ?? 0 }}</div>
          </div>
          <div class="queue-stat">
            <div class="queue-stat-label">Retry queue</div>
            <div class="queue-bar-wrap">
              <div class="queue-bar" :style="{ width: queueBarPct(metrics?.retry_queue_depth ?? 0, 20) + '%', background: '#f5a623' }"></div>
            </div>
            <div class="queue-stat-val">{{ metrics?.retry_queue_depth ?? 0 }}</div>
          </div>
          <div class="queue-stat">
            <div class="queue-stat-label">WS clients</div>
            <div class="queue-bar-wrap">
              <div class="queue-bar" :style="{ width: queueBarPct(metrics?.ws_clients ?? 0, 20) + '%', background: '#22d3a0' }"></div>
            </div>
            <div class="queue-stat-val">{{ metrics?.ws_clients ?? 0 }}</div>
          </div>
          <div class="queue-stat">
            <div class="queue-stat-label">Active workflows</div>
            <div class="queue-bar-wrap">
              <div class="queue-bar" :style="{ width: queueBarPct(metrics?.active_workflows ?? 0, 10) + '%', background: '#3b9eff' }"></div>
            </div>
            <div class="queue-stat-val">{{ metrics?.active_workflows ?? 0 }}</div>
          </div>
        </div>
      </div>
    </div>

    <!-- Execution table -->
    <div class="exec-table-card">
      <div class="table-header">
        <div class="chart-title" style="margin-bottom:0">Recent executions</div>
        <span class="table-count">{{ executions.length }} total</span>
      </div>
      <div class="table-scroll">
        <table class="exec-table">
          <thead>
            <tr>
              <th>Workflow</th>
              <th>Status</th>
              <th>Tasks</th>
              <th>Success rate</th>
              <th>Duration</th>
              <th>Started</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="exec in executions.slice(0, 20)" :key="exec.id">
              <td class="td-name">{{ exec.workflow_name }}</td>
              <td>
                <span class="badge" :class="exec.status">{{ exec.status }}</span>
              </td>
              <td class="td-mono">{{ exec.tasks?.length ?? 0 }}</td>
              <td>
                <div class="td-bar-wrap">
                  <div class="td-bar" :style="{ width: execSuccessRate(exec) + '%', background: execSuccessRate(exec) > 80 ? '#22d3a0' : '#f5a623' }"></div>
                  <span class="td-bar-label">{{ execSuccessRate(exec) }}%</span>
                </div>
              </td>
              <td class="td-mono">{{ execDuration(exec) }}</td>
              <td class="td-date">{{ new Date(exec.created_at).toLocaleTimeString() }}</td>
            </tr>
            <tr v-if="!executions.length">
              <td colspan="6" style="text-align:center;color:var(--text3);padding:24px">No executions yet</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import type { PlatformMetrics, WorkflowExecution } from '../../types'
import { STATUS_COLORS } from '../../types'

const props = defineProps<{
  metrics: PlatformMetrics | null
  executions: WorkflowExecution[]
}>()

// ── Sparkline ─────────────────────────────────────────────
const SPARK_W = 300
const SPARK_H = 80
const THROUGHPUT_WINDOW = 60
const sparkHistory = ref<number[]>(Array(20).fill(0))
let sparkTimer: ReturnType<typeof setInterval>

onMounted(() => {
  sparkTimer = setInterval(() => {
    const completed = props.metrics?.tasks_completed ?? 0
    sparkHistory.value.push(completed)
    if (sparkHistory.value.length > 20) sparkHistory.value.shift()
  }, 3000)
})
onUnmounted(() => clearInterval(sparkTimer))

const sparkPoints = computed(() => {
  const data = sparkHistory.value
  const min = Math.min(...data)
  const max = Math.max(...data, min + 1)
  return data.map((v, i) => ({
    x: (i / (data.length - 1)) * SPARK_W,
    y: SPARK_H - ((v - min) / (max - min)) * (SPARK_H - 10) - 5,
  }))
})

const sparkLinePath = computed(() => {
  const pts = sparkPoints.value
  if (pts.length < 2) return ''
  return pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')
})

const sparkAreaPath = computed(() => {
  const pts = sparkPoints.value
  if (pts.length < 2) return ''
  const line = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')
  return `${line} L${pts[pts.length-1].x},${SPARK_H} L${pts[0].x},${SPARK_H} Z`
})

// ── Donut chart ───────────────────────────────────────────
const statusCounts = computed(() => {
  const counts: Record<string, number> = {}
  props.executions.forEach(e => {
    e.tasks?.forEach(t => { counts[t.status] = (counts[t.status] ?? 0) + 1 })
  })
  return counts
})

const totalTasks = computed(() => Object.values(statusCounts.value).reduce((a, b) => a + b, 0))

const donutSegments = computed(() => {
  const total = totalTasks.value || 1
  const R = 38
  let angle = -Math.PI / 2
  const entries = Object.entries(statusCounts.value)

  return entries.map(([status, count]) => {
    const sweep = (count / total) * 2 * Math.PI
    const x1 = R * Math.cos(angle), y1 = R * Math.sin(angle)
    const x2 = R * Math.cos(angle + sweep), y2 = R * Math.sin(angle + sweep)
    const large = sweep > Math.PI ? 1 : 0
    const d = `M ${x1} ${y1} A ${R} ${R} 0 ${large} 1 ${x2} ${y2}`
    angle += sweep
    return { status, count, color: STATUS_COLORS[status as keyof typeof STATUS_COLORS] ?? '#9090a8', d }
  })
})

// ── KPI cards ─────────────────────────────────────────────
const kpiCards = computed(() => {
  const m = props.metrics
  const total = (m?.workflows_completed ?? 0) + (m?.workflows_failed ?? 0)
  const rate = total > 0 ? Math.round((m!.workflows_completed / total) * 100) : 100

  return [
    { label: 'Workflows started',   value: m?.workflows_started ?? 0,   color: 'var(--blue)',   sub: null, subColor: '' },
    { label: 'Success rate',        value: rate + '%',                   color: rate > 90 ? 'var(--green)' : 'var(--amber)', sub: `${m?.workflows_failed ?? 0} failed`, subColor: 'var(--red)' },
    { label: 'Tasks dispatched',    value: m?.tasks_dispatched ?? 0,     color: 'var(--purple)', sub: null, subColor: '' },
    { label: 'Tasks completed',     value: m?.tasks_completed ?? 0,      color: 'var(--green)',  sub: null, subColor: '' },
    { label: 'Tasks retried',       value: m?.tasks_retried ?? 0,        color: 'var(--amber)',  sub: null, subColor: '' },
    { label: 'Dead lettered',       value: m?.tasks_dead_lettered ?? 0,  color: 'var(--red)',    sub: null, subColor: '' },
  ]
})

// ── Helpers ───────────────────────────────────────────────
function queueBarPct(val: number, max: number) {
  return Math.min(100, Math.round((val / max) * 100))
}

function execSuccessRate(exec: WorkflowExecution) {
  const tasks = exec.tasks ?? []
  if (!tasks.length) return 100
  const ok = tasks.filter(t => t.status === 'completed').length
  return Math.round((ok / tasks.length) * 100)
}

function execDuration(exec: WorkflowExecution) {
  if (!exec.started_at) return '—'
  const ms = (exec.completed_at ? new Date(exec.completed_at) : new Date()).getTime()
           - new Date(exec.started_at).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms/1000).toFixed(1)}s`
  return `${Math.floor(ms/60000)}m ${Math.floor((ms%60000)/1000)}s`
}
</script>

<style scoped>
.metrics-view {
  flex: 1; overflow-y: auto; padding: 20px;
  display: flex; flex-direction: column; gap: 16px;
  background: var(--bg);
}
.metrics-view::-webkit-scrollbar { width: 4px; }
.metrics-view::-webkit-scrollbar-thumb { background: var(--border2); }

/* KPI row */
.kpi-row { display: grid; grid-template-columns: repeat(6, 1fr); gap: 10px; }

.kpi-card {
  background: var(--surface); border: 1px solid var(--border);
  border-radius: var(--radius-lg); padding: 14px 16px;
}
.kpi-val { font-size: 26px; font-weight: 700; font-family: var(--mono); line-height: 1; margin-bottom: 4px; }
.kpi-label { font-size: 10px; color: var(--text3); text-transform: uppercase; letter-spacing: .07em; }
.kpi-sub { font-size: 11px; margin-top: 6px; }

/* Charts row */
.charts-row { display: grid; grid-template-columns: 1fr 280px 280px; gap: 12px; }

.chart-card {
  background: var(--surface); border: 1px solid var(--border);
  border-radius: var(--radius-lg); padding: 16px;
}
.chart-title { font-size: 13px; font-weight: 600; margin-bottom: 3px; }
.chart-subtitle { font-size: 10px; color: var(--text3); margin-bottom: 14px; }

/* Sparkline */
.sparkline-wrap { display: flex; flex-direction: column; }
.spark-labels { display: flex; justify-content: space-between; font-size: 9px; color: var(--text3); margin-top: 4px; }

/* Donut */
.donut-wrap { display: flex; align-items: center; gap: 20px; }
.donut-legend { display: flex; flex-direction: column; gap: 6px; }
.legend-row { display: flex; align-items: center; gap: 7px; font-size: 11px; }
.legend-dot { width: 8px; height: 8px; border-radius: 2px; flex-shrink: 0; }
.legend-label { flex: 1; color: var(--text2); text-transform: capitalize; }
.legend-val { font-family: var(--mono); font-size: 11px; color: var(--text); font-weight: 600; }

/* Queue stats */
.queue-stats { display: flex; flex-direction: column; gap: 12px; }
.queue-stat { display: flex; align-items: center; gap: 10px; }
.queue-stat-label { font-size: 11px; color: var(--text3); width: 100px; flex-shrink: 0; }
.queue-bar-wrap { flex: 1; height: 5px; background: var(--surface2); border-radius: 3px; overflow: hidden; }
.queue-bar { height: 100%; border-radius: 3px; transition: width .5s ease; min-width: 2px; }
.queue-stat-val { font-family: var(--mono); font-size: 12px; font-weight: 600; width: 28px; text-align: right; }

/* Table */
.exec-table-card {
  background: var(--surface); border: 1px solid var(--border);
  border-radius: var(--radius-lg); overflow: hidden;
}
.table-header {
  padding: 12px 16px; border-bottom: 1px solid var(--border);
  display: flex; align-items: center; justify-content: space-between;
}
.table-count { font-size: 11px; color: var(--text3); }

.table-scroll { overflow-x: auto; }
.exec-table {
  width: 100%; border-collapse: collapse; font-size: 12px;
}
.exec-table th {
  padding: 8px 14px; text-align: left;
  font-size: 9px; font-weight: 600;
  text-transform: uppercase; letter-spacing: .07em;
  color: var(--text3);
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.exec-table td {
  padding: 8px 14px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.exec-table tr:last-child td { border-bottom: none; }
.exec-table tbody tr:hover td { background: rgba(255,255,255,.02); }

.td-name { font-weight: 500; max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.td-mono { font-family: var(--mono); font-size: 11px; color: var(--text2); }
.td-date { font-size: 10px; color: var(--text3); white-space: nowrap; }

.td-bar-wrap { display: flex; align-items: center; gap: 7px; }
.td-bar-label { font-size: 10px; font-family: var(--mono); color: var(--text2); width: 32px; }
.exec-table .td-bar-wrap { width: 120px; }
.exec-table .queue-bar-wrap { flex: 1; height: 4px; background: var(--surface2); border-radius: 2px; }
.exec-table .td-bar { height: 4px; border-radius: 2px; transition: width .4s; }

/* Status badge */
.badge {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 8px; border-radius: 20px;
  font-size: 10px; font-weight: 600;
  white-space: nowrap;
}
.badge::before { content:''; width:5px; height:5px; border-radius:50%; background: currentColor; }
.badge.pending   { background:rgba(144,144,168,.1); color:#9090a8; }
.badge.running   { background:rgba(59,158,255,.1); color:var(--blue); }
.badge.completed { background:rgba(34,211,160,.1); color:var(--green); }
.badge.failed    { background:rgba(255,95,87,.1); color:var(--red); }
</style>
