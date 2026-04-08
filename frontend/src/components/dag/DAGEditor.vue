<template>
  <div class="dag-editor">
    <!-- Toolbar -->
    <div class="dag-toolbar">
      <div class="toolbar-left">
        <input
          v-model="workflowName"
          placeholder="Workflow name…"
          class="name-input"
        />
        <select
          v-model="selectedTaskType"
          class="type-select"
        >
          <option value="">
            Add task…
          </option>
          <option
            v-for="t in TASK_TYPES"
            :key="t.value"
            :value="t.value"
          >
            {{ t.label }}
          </option>
        </select>
        <button
          :disabled="!selectedTaskType"
          class="btn-add"
          @click="addNode"
        >
          + Add Task
        </button>
      </div>
      <div class="toolbar-right">
        <button
          class="btn-secondary"
          @click="autoLayout"
        >
          Auto Layout
        </button>
        <button
          class="btn-secondary"
          @click="runValidation"
        >
          Validate
        </button>
        <button
          class="btn-primary"
          :disabled="saving"
          @click="saveWorkflow"
        >
          {{ saving ? 'Saving…' : 'Save' }}
        </button>
        <button
          v-if="savedWorkflowId"
          class="btn-run"
          @click="triggerRun"
        >
          ▶ Run
        </button>
      </div>
    </div>

    <!-- Validation banner -->
    <Transition name="banner">
      <div
        v-if="banner"
        class="banner"
        :class="banner.type"
      >
        {{ banner.text }}
      </div>
    </Transition>

    <!-- Flow canvas -->
    <div class="flow-wrap">
      <VueFlow
        v-model:nodes="nodes"
        v-model:edges="edges"
        :node-types="nodeTypes"
        :default-edge-options="defaultEdgeOptions"
        :connect-on-click="false"
        fit-view-on-init
        class="dark-flow"
        @connect="onConnect"
        @node-click="onNodeClick"
        @edge-click="onEdgeClick"
        @pane-click="onPaneClick"
      >
        <Background
          pattern-color="#2a2a40"
          :gap="20"
        />
        <Controls :show-fit-view="false" />
        <MiniMap
          node-color="#3a3a5a"
          mask-color="rgba(12,12,20,0.7)"
          :style="{ background: '#13131f' }"
        />

        <!-- Hint when canvas is empty -->
        <Panel position="top-center">
          <div
            v-if="nodes.length === 0"
            class="empty-hint"
          >
            Use <kbd>Add Task</kbd> above or drag from the sidebar to start building
          </div>
        </Panel>
      </VueFlow>
    </div>

    <!-- Right config panel -->
    <Transition name="slide-right">
      <div
        v-if="selectedNode"
        class="config-panel"
      >
        <div class="config-header">
          <span class="config-title">Configure Task</span>
          <button
            class="config-close"
            @click="selectedNode = null"
          >
            ✕
          </button>
        </div>
        <div class="config-body">
          <!-- Basic fields -->
          <div class="field">
            <label>Task Name</label>
            <input
              v-model="selectedNode.data.taskDef.name"
              class="cf-input"
            />
          </div>
          <div class="field">
            <label>Type</label>
            <select
              v-model="selectedNode.data.taskDef.type"
              class="cf-input"
            >
              <option
                v-for="t in TASK_TYPES"
                :key="t.value"
                :value="t.value"
              >
                {{ t.label }}
              </option>
            </select>
          </div>

          <!-- Per-type configuration -->
          <div class="section-label">
            Task Config
          </div>
          <TaskConfigFields
            :type="selectedNode.data.taskDef.type"
            :config="selectedNode.data.taskDef.config"
            @update="updateConfig"
          />

          <!-- Retry policy -->
          <div class="section-label">
            Retry Policy
          </div>
          <div class="field">
            <label>Max Retries</label>
            <input
              type="number"
              min="0"
              max="20"
              class="cf-input"
              :value="selectedNode.data.taskDef.retry_policy?.max_retries ?? 3"
              @change="setRetryField('max_retries', +($event.target as HTMLInputElement).value)"
            />
          </div>
          <div class="field">
            <label>Initial Delay (s)</label>
            <input
              type="number"
              min="0"
              step="0.5"
              class="cf-input"
              :value="(selectedNode.data.taskDef.retry_policy?.initial_delay ?? 2e9) / 1e9"
              @change="
                setRetryField('initial_delay', +($event.target as HTMLInputElement).value * 1e9)
              "
            />
          </div>
          <div class="field">
            <label>Backoff Multiplier</label>
            <input
              type="number"
              min="1"
              max="10"
              step="0.5"
              class="cf-input"
              :value="selectedNode.data.taskDef.retry_policy?.backoff_multiplier ?? 2"
              @change="
                setRetryField('backoff_multiplier', +($event.target as HTMLInputElement).value)
              "
            />
          </div>
          <div class="field">
            <label>Timeout (s)</label>
            <input
              type="number"
              min="0"
              class="cf-input"
              :value="(selectedNode.data.taskDef.timeout ?? 300e9) / 1e9"
              @change="
                selectedNode!.data.taskDef.timeout =
                  +($event.target as HTMLInputElement).value * 1e9
              "
            />
          </div>

          <!-- Dependencies -->
          <div class="section-label">
            Dependencies
          </div>
          <div class="deps-list">
            <span
              v-for="dep in selectedNode.data.taskDef.dependencies"
              :key="dep"
              class="dep-tag"
            >
              {{ nodeLabel(dep) }}
              <button @click="removeDep(dep)">×</button>
            </span>
            <span
              v-if="!selectedNode.data.taskDef.dependencies.length"
              class="no-deps"
            >
              No dependencies — this is a root task
            </span>
          </div>
          <p class="deps-hint">
            Connect nodes on the canvas to add dependencies
          </p>

          <button
            class="btn-delete"
            @click="deleteSelectedNode"
          >
            Delete Task
          </button>
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, markRaw, nextTick } from 'vue'
import { VueFlow, Panel, useVueFlow } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import type { Connection, EdgeMouseEvent, NodeMouseEvent } from '@vue-flow/core'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import { v4 as uuidv4 } from 'uuid'
import { useWorkflowStore } from '../../stores/workflow'
import { TASK_TYPES } from '../../types'
import type { DAGNode, DAGEdge, TaskDefinition } from '../../types'
import TaskNode from './TaskNode.vue'
import TaskConfigFields from './TaskConfigFields.vue'

const emit = defineEmits<{
  (e: 'triggered', execId: string): void
}>()

const store = useWorkflowStore()
const { fitView } = useVueFlow()

// ── State ──────────────────────────────────────────────────────
const workflowName = ref('New Workflow')
const selectedTaskType = ref('')
const selectedNode = ref<DAGNode | null>(null)
const saving = ref(false)
const savedWorkflowId = ref<string | null>(null)
const banner = ref<{ type: 'success' | 'error'; text: string } | null>(null)

const nodeTypes = { taskNode: markRaw(TaskNode) }
const defaultEdgeOptions = {
  type: 'smoothstep',
  animated: false,
  style: { stroke: '#4a4a6a', strokeWidth: 1.5 },
}

// Start with ONE empty generic node so the canvas is never blank
const initialId = uuidv4()
const nodes = ref<DAGNode[]>([
  {
    id: initialId,
    type: 'taskNode',
    position: { x: 300, y: 200 },
    data: {
      taskDef: {
        id: initialId,
        name: 'Start Task',
        type: 'generic',
        dependencies: [],
        config: {},
        retry_policy: {
          max_retries: 3,
          initial_delay: 2e9,
          max_delay: 300e9,
          backoff_multiplier: 2,
          jitter: true,
        },
        timeout: 300e9,
      },
    },
  },
])
const edges = ref<DAGEdge[]>([])

// ── Helpers ────────────────────────────────────────────────────
function nodeLabel(id: string) {
  return nodes.value.find((n) => n.id === id)?.data.taskDef.name ?? id
}

function showBanner(type: 'success' | 'error', text: string) {
  banner.value = { type, text }
  setTimeout(() => {
    banner.value = null
  }, 3500)
}

// ── Node actions ───────────────────────────────────────────────
function addNode() {
  if (!selectedTaskType.value) return
  const typeLabel =
    TASK_TYPES.find((t) => t.value === selectedTaskType.value)?.label ?? selectedTaskType.value
  const id = uuidv4()
  const node: DAGNode = {
    id,
    type: 'taskNode',
    position: { x: 120 + Math.random() * 500, y: 80 + nodes.value.length * 130 },
    data: {
      taskDef: {
        id,
        name: `${typeLabel} Task`,
        type: selectedTaskType.value,
        dependencies: [],
        config: defaultConfigForType(selectedTaskType.value),
        retry_policy: {
          max_retries: 3,
          initial_delay: 2e9,
          max_delay: 300e9,
          backoff_multiplier: 2,
          jitter: true,
        },
        timeout: 300e9,
      },
    },
  }
  nodes.value.push(node)
  selectedTaskType.value = ''
  selectedNode.value = node
}

function defaultConfigForType(type: string): Record<string, unknown> {
  switch (type) {
    case 'http_request':
      return { url: '', method: 'GET', headers: {}, body: '' }
    case 'database_query':
      return { query: '', connection_string: '' }
    case 'data_transform':
      return { script: '', input_format: 'json', output_format: 'json' }
    case 'ml_inference':
      return { model_name: '', input_path: '', output_path: '' }
    case 'notification':
      return { channel: '', message: '' }
    default:
      return { command: '', args: [] }
  }
}

function onNodeClick({ node }: NodeMouseEvent) {
  selectedNode.value = node as DAGNode
}

function onPaneClick() {
  selectedNode.value = null
}

function deleteSelectedNode() {
  if (!selectedNode.value) return
  const id = selectedNode.value.id
  nodes.value = nodes.value.filter((n) => n.id !== id)
  edges.value = edges.value.filter((e) => e.source !== id && e.target !== id)
  nodes.value.forEach((n) => {
    n.data.taskDef.dependencies = n.data.taskDef.dependencies.filter((d) => d !== id)
  })
  selectedNode.value = null
}

// ── Edge / connection ──────────────────────────────────────────
function onConnect(connection: Connection) {
  if (!connection.source || !connection.target) return
  // Prevent self-loops
  if (connection.source === connection.target) return
  // Prevent duplicate edges
  const exists = edges.value.some(
    (e) => e.source === connection.source && e.target === connection.target,
  )
  if (exists) return

  const edge: DAGEdge = {
    id: `e-${connection.source}-${connection.target}`,
    source: connection.source,
    target: connection.target,
    type: 'smoothstep',
  }
  edges.value.push(edge)

  // Keep task dependencies in sync with edges
  const targetNode = nodes.value.find((n) => n.id === connection.target)
  if (targetNode && !targetNode.data.taskDef.dependencies.includes(connection.source)) {
    targetNode.data.taskDef.dependencies.push(connection.source)
  }
}

function onEdgeClick({ edge }: EdgeMouseEvent) {
  // Click an edge to delete it
  edges.value = edges.value.filter((e) => e.id !== edge.id)
  const targetNode = nodes.value.find((n) => n.id === edge.target)
  if (targetNode) {
    targetNode.data.taskDef.dependencies = targetNode.data.taskDef.dependencies.filter(
      (d) => d !== edge.source,
    )
  }
}

function removeDep(depId: string) {
  if (!selectedNode.value) return
  selectedNode.value.data.taskDef.dependencies =
    selectedNode.value.data.taskDef.dependencies.filter((d) => d !== depId)
  edges.value = edges.value.filter(
    (e) => !(e.source === depId && e.target === selectedNode.value!.id),
  )
}

// ── Config helpers ─────────────────────────────────────────────
function updateConfig(newConfig: Record<string, unknown>) {
  if (selectedNode.value) {
    selectedNode.value.data.taskDef.config = newConfig
  }
}

function setRetryField(field: keyof NonNullable<TaskDefinition['retry_policy']>, value: number) {
  if (!selectedNode.value) return
  const policy = selectedNode.value.data.taskDef.retry_policy ?? {
    max_retries: 3,
    initial_delay: 2e9,
    max_delay: 300e9,
    backoff_multiplier: 2,
    jitter: true,
  }
  selectedNode.value.data.taskDef.retry_policy = { ...policy, [field]: value }
}

// ── Layout ─────────────────────────────────────────────────────
function autoLayout() {
  const inDegree = new Map<string, number>()
  const adjList = new Map<string, string[]>()
  nodes.value.forEach((n) => {
    inDegree.set(n.id, 0)
    adjList.set(n.id, [])
  })
  edges.value.forEach((e) => {
    inDegree.set(e.target, (inDegree.get(e.target) ?? 0) + 1)
    adjList.get(e.source)?.push(e.target)
  })

  const queue = nodes.value.filter((n) => (inDegree.get(n.id) ?? 0) === 0).map((n) => n.id)
  const levelMap = new Map<string, number>()
  const order: string[] = []

  while (queue.length) {
    const id = queue.shift()!
    order.push(id)
    for (const next of adjList.get(id) ?? []) {
      const deg = (inDegree.get(next) ?? 1) - 1
      inDegree.set(next, deg)
      if (deg === 0) queue.push(next)
    }
  }

  order.forEach((id) => {
    const node = nodes.value.find((n) => n.id === id)!
    const depLevel = Math.max(
      0,
      ...node.data.taskDef.dependencies.map((d) => (levelMap.get(d) ?? 0) + 1),
    )
    levelMap.set(id, depLevel)
  })

  const byLevel = new Map<number, string[]>()
  levelMap.forEach((lv, id) => {
    if (!byLevel.has(lv)) byLevel.set(lv, [])
    byLevel.get(lv)!.push(id)
  })

  byLevel.forEach((ids, level) => {
    ids.forEach((id, i) => {
      const node = nodes.value.find((n) => n.id === id)!
      const totalW = ids.length * 240
      node.position = { x: -totalW / 2 + i * 240 + 120, y: level * 160 + 60 }
    })
  })

  nextTick(() => fitView({ padding: 0.2 }))
}

// ── Validate ───────────────────────────────────────────────────
function runValidation(): boolean {
  const visited = new Set<string>()
  const inStack = new Set<string>()
  const adj = new Map<string, string[]>()
  nodes.value.forEach((n) => adj.set(n.id, []))
  edges.value.forEach((e) => adj.get(e.source)?.push(e.target))

  function hasCycle(id: string): boolean {
    visited.add(id)
    inStack.add(id)
    for (const nbr of adj.get(id) ?? []) {
      if (!visited.has(nbr) && hasCycle(nbr)) return true
      if (inStack.has(nbr)) return true
    }
    inStack.delete(id)
    return false
  }

  for (const node of nodes.value) {
    if (!visited.has(node.id) && hasCycle(node.id)) {
      showBanner('error', '⚠ Cycle detected — DAG is invalid')
      return false
    }
  }
  showBanner('success', `✓ Valid DAG — ${nodes.value.length} tasks, ${edges.value.length} edges`)
  return true
}

// ── Save & run ─────────────────────────────────────────────────
async function saveWorkflow() {
  if (!runValidation()) return
  saving.value = true
  try {
    const def = {
      name: workflowName.value,
      description: '',
      version: '1.0.0',
      tasks: nodes.value.map((n) => n.data.taskDef),
      max_parallel: 10,
    }
    const created = await store.createDefinition(def)
    savedWorkflowId.value = created.id
    showBanner('success', `✓ Saved "${created.name}"`)
  } catch {
    showBanner('error', 'Save failed — check backend connection')
  } finally {
    saving.value = false
  }
}

async function triggerRun() {
  if (!savedWorkflowId.value) return
  try {
    const exec = await store.triggerWorkflow(savedWorkflowId.value, {})
    emit('triggered', exec.id)
    showBanner('success', `▶ Run started: ${exec.id.slice(0, 8)}…`)
  } catch {
    showBanner('error', 'Failed to trigger run')
  }
}

// ── Expose for parent (e.g. BuilderPage loading an existing wf) ─
defineExpose({
  loadWorkflow(wfName: string, tasks: TaskDefinition[]) {
    workflowName.value = wfName
    nodes.value = tasks.map((t, i) => ({
      id: t.id,
      type: 'taskNode' as const,
      position: { x: 120 + (i % 3) * 240, y: Math.floor(i / 3) * 160 + 60 },
      data: { taskDef: { ...t } },
    }))
    edges.value = tasks.flatMap((t) =>
      (t.dependencies ?? []).map((dep) => ({
        id: `e-${dep}-${t.id}`,
        source: dep,
        target: t.id,
        type: 'smoothstep' as const,
        animated: false,
      })),
    )
    nextTick(() => fitView({ padding: 0.2 }))
  },
  resetToEmpty() {
    workflowName.value = 'New Workflow'
    const id = uuidv4()
    nodes.value = [
      {
        id,
        type: 'taskNode',
        position: { x: 300, y: 200 },
        data: {
          taskDef: {
            id,
            name: 'Start Task',
            type: 'generic',
            dependencies: [],
            config: { command: '', args: [] },
            retry_policy: {
              max_retries: 3,
              initial_delay: 2e9,
              max_delay: 300e9,
              backoff_multiplier: 2,
              jitter: true,
            },
            timeout: 300e9,
          },
        },
      },
    ]
    edges.value = []
    savedWorkflowId.value = null
    selectedNode.value = null
  },
})
</script>

<style>
@import '../../../node_modules/@vue-flow/core/dist/style.css';
@import '../../../node_modules/@vue-flow/controls/dist/style.css';

.dag-editor {
  display: flex;
  flex-direction: column;
  height: 100%;
  position: relative;
  background: var(--bg3);
}

/* Toolbar */
.dag-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 14px;
  background: var(--bg2);
  border-bottom: 1px solid var(--border);
  gap: 10px;
  flex-wrap: wrap;
  flex-shrink: 0;
}
.toolbar-left,
.toolbar-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.name-input {
  font-size: 13px;
  font-weight: 600;
  background: var(--surface);
  border: 1px solid var(--border2);
  color: var(--text);
  border-radius: var(--r-sm);
  padding: 6px 10px;
  outline: none;
  width: 200px;
}
.name-input:focus {
  border-color: var(--accent);
}

.type-select {
  font-size: 12px;
  background: var(--surface);
  border: 1px solid var(--border2);
  color: var(--text2);
  border-radius: var(--r-sm);
  padding: 6px 10px;
  outline: none;
}

.btn-add {
  padding: 6px 13px;
  background: rgba(124, 106, 255, 0.15);
  color: var(--accent);
  border: 1px solid rgba(124, 106, 255, 0.3);
  border-radius: var(--r-sm);
  font-size: 12px;
  font-weight: 500;
}
.btn-add:hover {
  background: rgba(124, 106, 255, 0.25);
}
.btn-add:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.btn-secondary {
  padding: 6px 13px;
  background: var(--surface);
  color: var(--text2);
  border: 1px solid var(--border2);
  border-radius: var(--r-sm);
  font-size: 12px;
}
.btn-secondary:hover {
  background: var(--surface2);
  color: var(--text);
}

.btn-primary {
  padding: 6px 15px;
  background: var(--accent);
  color: #fff;
  border: none;
  border-radius: var(--r-sm);
  font-size: 12px;
  font-weight: 500;
}
.btn-primary:hover {
  background: #5b4bd4;
}
.btn-primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.btn-run {
  padding: 6px 15px;
  background: rgba(34, 211, 160, 0.15);
  color: var(--green);
  border: 1px solid rgba(34, 211, 160, 0.3);
  border-radius: var(--r-sm);
  font-size: 12px;
  font-weight: 600;
}
.btn-run:hover {
  background: rgba(34, 211, 160, 0.25);
}

/* Banner */
.banner {
  padding: 7px 16px;
  font-size: 12px;
  font-weight: 500;
  flex-shrink: 0;
}
.banner.success {
  background: rgba(34, 211, 160, 0.1);
  color: var(--green);
  border-bottom: 1px solid rgba(34, 211, 160, 0.2);
}
.banner.error {
  background: rgba(255, 95, 87, 0.1);
  color: var(--red);
  border-bottom: 1px solid rgba(255, 95, 87, 0.2);
}
.banner-enter-active,
.banner-leave-active {
  transition: all 0.2s ease;
  overflow: hidden;
}
.banner-enter-from,
.banner-leave-to {
  max-height: 0;
  opacity: 0;
}
.banner-enter-to,
.banner-leave-from {
  max-height: 40px;
}

/* Canvas */
.flow-wrap {
  flex: 1;
  overflow: hidden;
}
.dark-flow {
  background: #0c0c14;
}

/* Empty hint */
.empty-hint {
  background: rgba(124, 106, 255, 0.1);
  border: 1px dashed rgba(124, 106, 255, 0.3);
  color: var(--text3);
  font-size: 12px;
  padding: 8px 16px;
  border-radius: var(--r);
  margin-top: 12px;
}
.empty-hint kbd {
  background: var(--surface);
  border: 1px solid var(--border2);
  border-radius: 4px;
  padding: 1px 5px;
  font-size: 11px;
  color: var(--text2);
}

/* Config panel */
.config-panel {
  position: absolute;
  right: 0;
  top: 0;
  bottom: 0;
  width: 290px;
  background: var(--bg2);
  border-left: 1px solid var(--border2);
  z-index: 20;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  box-shadow: -6px 0 24px rgba(0, 0, 0, 0.4);
}
.config-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}
.config-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.config-close {
  background: none;
  border: none;
  color: var(--text3);
  font-size: 15px;
  cursor: pointer;
}
.config-close:hover {
  color: var(--text);
}

.config-body {
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  flex: 1;
}

.section-label {
  font-size: 9px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text3);
  margin-top: 4px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.field label {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text3);
}
.cf-input {
  font-size: 12px;
  background: var(--surface);
  border: 1px solid var(--border2);
  color: var(--text);
  border-radius: var(--r-sm);
  padding: 6px 9px;
  outline: none;
}
.cf-input:focus {
  border-color: var(--accent);
}

.deps-list {
  display: flex;
  gap: 5px;
  flex-wrap: wrap;
  min-height: 28px;
}
.dep-tag {
  display: flex;
  align-items: center;
  gap: 4px;
  background: rgba(124, 106, 255, 0.12);
  color: var(--accent);
  border-radius: 4px;
  padding: 2px 7px;
  font-size: 11px;
}
.dep-tag button {
  background: none;
  border: none;
  cursor: pointer;
  color: var(--accent);
  padding: 0;
  font-size: 13px;
  line-height: 1;
}
.no-deps {
  font-size: 11px;
  color: var(--text3);
}
.deps-hint {
  font-size: 10px;
  color: var(--text3);
  line-height: 1.4;
}

.btn-delete {
  padding: 7px 14px;
  background: rgba(255, 95, 87, 0.08);
  color: var(--red);
  border: 1px solid rgba(255, 95, 87, 0.25);
  border-radius: var(--r-sm);
  font-size: 12px;
  margin-top: 6px;
}
.btn-delete:hover {
  background: rgba(255, 95, 87, 0.15);
}

/* Slide transition */
.slide-right-enter-active,
.slide-right-leave-active {
  transition: transform 0.2s ease;
}
.slide-right-enter-from,
.slide-right-leave-to {
  transform: translateX(100%);
}

/* Flow control buttons */
.vue-flow__controls {
  display: flex;
}
.vue-flow__controls-button {
  width: 16px !important;
}
</style>
