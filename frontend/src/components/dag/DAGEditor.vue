<template>
  <div class="dag-editor">
    <!-- Toolbar -->
    <div class="dag-toolbar">
      <div class="toolbar-left">
        <input
          v-model="workflowName"
          placeholder="Workflow name..."
          class="name-input"
        />
        <select v-model="selectedTaskType" class="type-select">
          <option value="">Add task type...</option>
          <option v-for="t in TASK_TYPES" :key="t.value" :value="t.value">
            {{ t.label }}
          </option>
        </select>
        <button @click="addNode" :disabled="!selectedTaskType" class="btn-add">
          + Add Task
        </button>
      </div>
      <div class="toolbar-right">
        <button @click="autoLayout" class="btn-secondary">Auto Layout</button>
        <button @click="validateDAG" class="btn-secondary">Validate</button>
        <button @click="saveWorkflow" class="btn-primary" :disabled="saving">
          {{ saving ? 'Saving...' : 'Save Workflow' }}
        </button>
        <button v-if="workflowId" @click="triggerRun" class="btn-run">
          ▶ Run
        </button>
      </div>
    </div>

    <!-- Validation messages -->
    <div v-if="validationMsg" class="validation-banner" :class="validationMsg.type">
      {{ validationMsg.text }}
    </div>

    <!-- Canvas -->
    <div class="flow-canvas">
      <VueFlow
        v-model:nodes="nodes"
        v-model:edges="edges"
        :nodeTypes="nodeTypes"
        :defaultEdgeOptions="defaultEdgeOptions"
        fit-view-on-init
        @connect="onConnect"
        @node-click="onNodeClick"
        @edge-click="onEdgeClick"
      >
        <Background />
        <Controls />
        <MiniMap />
      </VueFlow>
    </div>

    <!-- Node config panel -->
    <Transition name="slide">
      <div v-if="selectedNode" class="config-panel">
        <div class="config-header">
          <h3>Configure Task</h3>
          <button @click="selectedNode = null" class="btn-close">✕</button>
        </div>
        <div class="config-body">
          <div class="field">
            <label>Task Name</label>
            <input v-model="selectedNode.data.taskDef.name" class="config-input" />
          </div>
          <div class="field">
            <label>Type</label>
            <select v-model="selectedNode.data.taskDef.type" class="config-input">
              <option v-for="t in TASK_TYPES" :key="t.value" :value="t.value">{{ t.label }}</option>
            </select>
          </div>
          <div class="field">
            <label>Dependencies</label>
            <div class="deps-list">
              <span v-for="dep in selectedNode.data.taskDef.dependencies" :key="dep" class="dep-tag">
                {{ getNodeName(dep) }}
                <button @click="removeDep(dep)">×</button>
              </span>
              <span v-if="!selectedNode.data.taskDef.dependencies.length" class="no-deps">
                None (root task)
              </span>
            </div>
          </div>
          <div class="field">
            <label>Max Retries</label>
            <input
              type="number"
              min="0" max="10"
              :value="selectedNode.data.taskDef.retry_policy?.max_retries ?? 3"
              @input="setMaxRetries($event)"
              class="config-input"
            />
          </div>
          <div class="field">
            <label>Timeout (seconds)</label>
            <input
              type="number"
              min="0"
              :value="selectedNode.data.taskDef.timeout ? selectedNode.data.taskDef.timeout / 1e9 : 300"
              @input="setTimeout_($event)"
              class="config-input"
            />
          </div>
          <button @click="deleteNode" class="btn-delete">Delete Task</button>
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, markRaw } from 'vue'
import { VueFlow, useVueFlow, Background, Controls, MiniMap } from '@vue-flow/core'
import '@vue-flow/core/dist/style.css'
import { v4 as uuidv4 } from 'uuid'
import { useWorkflowStore } from '../../stores/workflow'
import { TASK_TYPES } from '../../types'
import type { DAGNode, DAGEdge, TaskDefinition } from '../../types'
import TaskNode from './TaskNode.vue'

const emit = defineEmits<{ (e: 'triggered', execId: string): void }>()

const store = useWorkflowStore()
const { addEdges, removeEdges, fitView } = useVueFlow()

const nodes = ref<DAGNode[]>([])
const edges = ref<DAGEdge[]>([])
const workflowName = ref('My Workflow')
const selectedTaskType = ref('')
const selectedNode = ref<DAGNode | null>(null)
const saving = ref(false)
const workflowId = ref<string | null>(null)
const validationMsg = ref<{ type: 'success' | 'error'; text: string } | null>(null)

const nodeTypes = { taskNode: markRaw(TaskNode) }

const defaultEdgeOptions = {
  type: 'smoothstep',
  animated: false,
  style: { stroke: '#94a3b8', strokeWidth: 2 },
}

function addNode() {
  if (!selectedTaskType.value) return
  const id = uuidv4()
  const typeLabel = TASK_TYPES.find(t => t.value === selectedTaskType.value)?.label || selectedTaskType.value

  const node: DAGNode = {
    id,
    type: 'taskNode',
    position: {
      x: 100 + Math.random() * 400,
      y: 100 + nodes.value.length * 120,
    },
    data: {
      taskDef: {
        id,
        name: `${typeLabel} Task`,
        type: selectedTaskType.value,
        dependencies: [],
        config: {},
        retry_policy: { max_retries: 3, initial_delay: 2e9, max_delay: 300e9, backoff_multiplier: 2, jitter: true },
        timeout: 300e9,
      },
    },
  }
  nodes.value.push(node)
  selectedTaskType.value = ''
}

function onConnect(connection: { source: string; target: string }) {
  const edge: DAGEdge = {
    id: `e-${connection.source}-${connection.target}`,
    source: connection.source,
    target: connection.target,
    type: 'smoothstep',
  }
  edges.value.push(edge)

  // Update dependency in target node
  const targetNode = nodes.value.find(n => n.id === connection.target)
  if (targetNode && !targetNode.data.taskDef.dependencies.includes(connection.source)) {
    targetNode.data.taskDef.dependencies.push(connection.source)
  }
}

function onNodeClick({ node }: { node: DAGNode }) {
  selectedNode.value = node
}

function onEdgeClick({ edge }: { edge: DAGEdge }) {
  edges.value = edges.value.filter(e => e.id !== edge.id)
  const targetNode = nodes.value.find(n => n.id === edge.target)
  if (targetNode) {
    targetNode.data.taskDef.dependencies = targetNode.data.taskDef.dependencies.filter(d => d !== edge.source)
  }
}

function deleteNode() {
  if (!selectedNode.value) return
  const id = selectedNode.value.id
  nodes.value = nodes.value.filter(n => n.id !== id)
  edges.value = edges.value.filter(e => e.source !== id && e.target !== id)
  nodes.value.forEach(n => {
    n.data.taskDef.dependencies = n.data.taskDef.dependencies.filter(d => d !== id)
  })
  selectedNode.value = null
}

function removeDep(depId: string) {
  if (!selectedNode.value) return
  selectedNode.value.data.taskDef.dependencies = selectedNode.value.data.taskDef.dependencies.filter(d => d !== depId)
  edges.value = edges.value.filter(e => !(e.source === depId && e.target === selectedNode.value!.id))
}

function setMaxRetries(event: Event) {
  if (!selectedNode.value) return
  const val = parseInt((event.target as HTMLInputElement).value)
  if (!selectedNode.value.data.taskDef.retry_policy) {
    selectedNode.value.data.taskDef.retry_policy = { max_retries: val, initial_delay: 2e9, max_delay: 300e9, backoff_multiplier: 2, jitter: true }
  } else {
    selectedNode.value.data.taskDef.retry_policy.max_retries = val
  }
}

function setTimeout_(event: Event) {
  if (!selectedNode.value) return
  const secs = parseInt((event.target as HTMLInputElement).value)
  selectedNode.value.data.taskDef.timeout = secs * 1e9
}

function getNodeName(id: string) {
  return nodes.value.find(n => n.id === id)?.data.taskDef.name || id
}

function autoLayout() {
  const levelMap = new Map<string, number>()
  const topologicalOrder: string[] = []

  // Kahn's BFS
  const inDegree = new Map<string, number>()
  const adjList = new Map<string, string[]>()
  nodes.value.forEach(n => { inDegree.set(n.id, 0); adjList.set(n.id, []) })
  edges.value.forEach(e => {
    inDegree.set(e.target, (inDegree.get(e.target) || 0) + 1)
    adjList.get(e.source)?.push(e.target)
  })

  const queue = nodes.value.filter(n => inDegree.get(n.id) === 0).map(n => n.id)
  while (queue.length) {
    const id = queue.shift()!
    topologicalOrder.push(id)
    for (const dep of adjList.get(id) || []) {
      const newDeg = (inDegree.get(dep) || 1) - 1
      inDegree.set(dep, newDeg)
      if (newDeg === 0) queue.push(dep)
    }
  }

  // Assign levels
  topologicalOrder.forEach(id => {
    const node = nodes.value.find(n => n.id === id)!
    const maxDepLevel = Math.max(0, ...node.data.taskDef.dependencies.map(d => (levelMap.get(d) || 0) + 1))
    levelMap.set(id, maxDepLevel)
  })

  // Group by level
  const byLevel = new Map<number, string[]>()
  levelMap.forEach((level, id) => {
    if (!byLevel.has(level)) byLevel.set(level, [])
    byLevel.get(level)!.push(id)
  })

  // Position nodes
  byLevel.forEach((ids, level) => {
    ids.forEach((id, i) => {
      const node = nodes.value.find(n => n.id === id)!
      const totalWidth = ids.length * 220
      node.position = {
        x: -totalWidth / 2 + i * 220 + 110,
        y: level * 160 + 40,
      }
    })
  })

  setTimeout(() => fitView({ padding: 0.2 }), 50)
}

function validateDAG(): boolean {
  // Check for cycles using DFS
  const visited = new Set<string>()
  const inStack = new Set<string>()
  const adj = new Map<string, string[]>()
  nodes.value.forEach(n => adj.set(n.id, []))
  edges.value.forEach(e => adj.get(e.source)?.push(e.target))

  function hasCycle(id: string): boolean {
    visited.add(id)
    inStack.add(id)
    for (const neighbor of adj.get(id) || []) {
      if (!visited.has(neighbor)) {
        if (hasCycle(neighbor)) return true
      } else if (inStack.has(neighbor)) {
        return true
      }
    }
    inStack.delete(id)
    return false
  }

  for (const node of nodes.value) {
    if (!visited.has(node.id) && hasCycle(node.id)) {
      validationMsg.value = { type: 'error', text: '⚠ Cycle detected in workflow graph' }
      setTimeout(() => validationMsg.value = null, 4000)
      return false
    }
  }

  validationMsg.value = { type: 'success', text: '✓ DAG is valid — no cycles detected' }
  setTimeout(() => validationMsg.value = null, 3000)
  return true
}

async function saveWorkflow() {
  if (!validateDAG()) return
  saving.value = true
  try {
    const def = {
      name: workflowName.value,
      description: '',
      version: '1.0.0',
      tasks: nodes.value.map(n => n.data.taskDef),
      max_parallel: 10,
    }
    const created = await store.createDefinition(def)
    workflowId.value = created.id
    validationMsg.value = { type: 'success', text: `✓ Workflow "${created.name}" saved` }
    setTimeout(() => validationMsg.value = null, 3000)
  } catch (e) {
    validationMsg.value = { type: 'error', text: 'Failed to save workflow' }
  } finally {
    saving.value = false
  }
}

async function triggerRun() {
  if (!workflowId.value) return
  try {
    const exec = await store.triggerWorkflow(workflowId.value, {})
    emit('triggered', exec.id)
  } catch (e) {
    validationMsg.value = { type: 'error', text: 'Failed to trigger workflow' }
  }
}

// Expose load function for populating from templates
defineExpose({
  loadWorkflow(tasks: TaskDefinition[]) {
    nodes.value = tasks.map((t, i) => ({
      id: t.id,
      type: 'taskNode' as const,
      position: { x: 100 + (i % 3) * 220, y: Math.floor(i / 3) * 160 + 40 },
      data: { taskDef: t },
    }))
    edges.value = tasks.flatMap(t =>
      (t.dependencies || []).map(dep => ({
        id: `e-${dep}-${t.id}`,
        source: dep,
        target: t.id,
        type: 'smoothstep' as const,
        animated: false,
      }))
    )
    setTimeout(() => fitView({ padding: 0.2 }), 100)
  }
})
</script>

<style scoped>
.dag-editor { display: flex; flex-direction: column; height: 100%; position: relative; }

.dag-toolbar {
  display: flex; align-items: center; justify-content: space-between;
  padding: 10px 16px; background: #fff;
  border-bottom: 1px solid #e5e7eb; gap: 12px; flex-wrap: wrap;
}

.toolbar-left, .toolbar-right { display: flex; align-items: center; gap: 8px; }

.name-input {
  font-size: 14px; font-weight: 600; border: 1px solid #e5e7eb;
  border-radius: 6px; padding: 6px 10px; outline: none; width: 200px;
}
.name-input:focus { border-color: #6366f1; }

.type-select {
  font-size: 13px; border: 1px solid #e5e7eb; border-radius: 6px;
  padding: 6px 10px; outline: none; background: #fff;
}

.btn-add { padding: 6px 14px; background: #f0f0ff; color: #4f46e5; border: 1px solid #c7d2fe; border-radius: 6px; font-size: 13px; font-weight: 500; cursor: pointer; }
.btn-add:hover { background: #e0e7ff; }
.btn-add:disabled { opacity: 0.5; cursor: not-allowed; }

.btn-secondary { padding: 6px 14px; background: #f9fafb; color: #374151; border: 1px solid #e5e7eb; border-radius: 6px; font-size: 13px; cursor: pointer; }
.btn-secondary:hover { background: #f3f4f6; }

.btn-primary { padding: 6px 16px; background: #4f46e5; color: #fff; border: none; border-radius: 6px; font-size: 13px; font-weight: 500; cursor: pointer; }
.btn-primary:hover { background: #4338ca; }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }

.btn-run { padding: 6px 16px; background: #059669; color: #fff; border: none; border-radius: 6px; font-size: 13px; font-weight: 500; cursor: pointer; }
.btn-run:hover { background: #047857; }

.validation-banner {
  padding: 8px 16px; font-size: 13px; font-weight: 500;
  animation: slideDown 0.2s ease;
}
.validation-banner.success { background: #f0fdf4; color: #166534; border-bottom: 1px solid #86efac; }
.validation-banner.error { background: #fef2f2; color: #991b1b; border-bottom: 1px solid #fca5a5; }

.flow-canvas { flex: 1; }

/* Config panel */
.config-panel {
  position: absolute; right: 0; top: 58px; bottom: 0;
  width: 280px; background: #fff;
  border-left: 1px solid #e5e7eb;
  z-index: 10; overflow-y: auto;
  box-shadow: -4px 0 16px rgba(0,0,0,0.06);
}

.config-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 16px; border-bottom: 1px solid #e5e7eb;
}
.config-header h3 { margin: 0; font-size: 14px; font-weight: 600; color: #111827; }

.btn-close { background: none; border: none; font-size: 16px; cursor: pointer; color: #6b7280; }
.btn-close:hover { color: #111827; }

.config-body { padding: 16px; display: flex; flex-direction: column; gap: 14px; }

.field { display: flex; flex-direction: column; gap: 5px; }
.field label { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: #6b7280; }

.config-input {
  font-size: 13px; border: 1px solid #e5e7eb; border-radius: 6px;
  padding: 7px 10px; outline: none;
}
.config-input:focus { border-color: #6366f1; }

.deps-list { display: flex; gap: 4px; flex-wrap: wrap; padding: 6px 0; }
.dep-tag {
  display: flex; align-items: center; gap: 4px;
  background: #ede9fe; color: #5b21b6;
  border-radius: 4px; padding: 2px 6px; font-size: 11px;
}
.dep-tag button { background: none; border: none; cursor: pointer; font-size: 12px; color: #7c3aed; padding: 0; line-height: 1; }
.no-deps { font-size: 12px; color: #9ca3af; }

.btn-delete { padding: 7px 14px; background: #fef2f2; color: #dc2626; border: 1px solid #fca5a5; border-radius: 6px; font-size: 13px; cursor: pointer; margin-top: 8px; }
.btn-delete:hover { background: #fee2e2; }

.slide-enter-active, .slide-leave-active { transition: transform 0.2s ease; }
.slide-enter-from, .slide-leave-to { transform: translateX(100%); }

@keyframes slideDown {
  from { transform: translateY(-8px); opacity: 0; }
  to { transform: translateY(0); opacity: 1; }
}
</style>
