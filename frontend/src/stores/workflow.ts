import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { WorkflowDefinition, WorkflowExecution, PlatformMetrics } from '../types'
import { api } from '../composables/useApi'

export const useWorkflowStore = defineStore('workflows', () => {
  // State
  const definitions = ref<WorkflowDefinition[]>([])
  const executions = ref<WorkflowExecution[]>([])
  const selectedExecution = ref<WorkflowExecution | null>(null)
  const metrics = ref<PlatformMetrics | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // Actions
  async function fetchDefinitions() {
    try {
      loading.value = true
      const data = await api.get<{ workflows: WorkflowDefinition[] }>('/api/workflows')
      definitions.value = data.workflows || []
    } catch (err) {
      error.value = 'Failed to load workflows'
      console.error(err)
    } finally {
      loading.value = false
    }
  }

  async function createDefinition(
    def: Omit<WorkflowDefinition, 'id' | 'created_at' | 'updated_at'>,
  ) {
    try {
      const created = await api.post<WorkflowDefinition>('/api/workflows', def)
      definitions.value.unshift(created)
      return created
    } catch (err) {
      error.value = 'Failed to create workflow'
      throw err
    }
  }

  async function fetchExecutions(limit = 50, offset = 0) {
    try {
      loading.value = true
      const data = await api.get<{ executions: WorkflowExecution[] }>(
        `/api/executions?limit=${limit}&offset=${offset}`,
      )
      executions.value = data.executions || []
    } catch (err) {
      error.value = 'Failed to load executions'
      console.error(err)
    } finally {
      loading.value = false
    }
  }

  async function fetchExecution(id: string) {
    try {
      const exec = await api.get<WorkflowExecution>(`/api/executions/${id}`)
      selectedExecution.value = exec

      // Update in list if present
      const idx = executions.value.findIndex((e) => e.id === id)
      if (idx >= 0) {
        executions.value[idx] = exec
      }
      return exec
    } catch (err) {
      error.value = 'Failed to load execution'
      throw err
    }
  }

  async function triggerWorkflow(workflowId: string, payload?: Record<string, unknown>) {
    try {
      const exec = await api.post<WorkflowExecution>(
        `/api/workflows/${workflowId}/trigger`,
        payload || {},
      )
      executions.value.unshift(exec)
      selectedExecution.value = exec
      return exec
    } catch (err) {
      error.value = 'Failed to trigger workflow'
      throw err
    }
  }

  async function retryExecution(execId: string) {
    try {
      const newExec = await api.post<WorkflowExecution>(`/api/executions/${execId}/retry`, {})
      executions.value.unshift(newExec)
      selectedExecution.value = newExec
      return newExec
    } catch (err) {
      error.value = 'Failed to retry execution'
      throw err
    }
  }

  async function cancelExecution(execId: string) {
    try {
      await api.post<void>(`/api/executions/${execId}/cancel`, {})
      const exec = executions.value.find((e) => e.id === execId)
      if (exec) exec.status = 'cancelled'
    } catch (err) {
      error.value = 'Failed to cancel execution'
      throw err
    }
  }

  async function fetchMetrics() {
    try {
      metrics.value = await api.get<PlatformMetrics>('/api/metrics')
    } catch (err) {
      console.error('Failed to fetch metrics:', err)
    }
  }

  // Update from WebSocket events
  function updateFromWsEvent(type: string, payload: unknown) {
    if (type.startsWith('workflow.')) {
      const exec = payload as WorkflowExecution
      const idx = executions.value.findIndex((e) => e.id === exec.id)
      if (idx >= 0) {
        executions.value[idx] = { ...executions.value[idx], ...exec }
      } else {
        executions.value.unshift(exec)
      }
      if (selectedExecution.value?.id === exec.id) {
        selectedExecution.value = { ...selectedExecution.value, ...exec }
      }
    }

    if (type.startsWith('task.')) {
      const task = payload as { id: string; workflow_exec_id: string; status: string }
      // Update task in selectedExecution if it matches
      if (selectedExecution.value?.id === task.workflow_exec_id) {
        const taskIdx = selectedExecution.value.tasks.findIndex((t) => t.id === task.id)
        if (taskIdx >= 0) {
          Object.assign(selectedExecution.value.tasks[taskIdx], task)
        }
      }

      // Also update in the executions list
      const exec = executions.value.find((e) => e.id === task.workflow_exec_id)
      if (exec) {
        const tIdx = exec.tasks.findIndex((t) => t.id === task.id)
        if (tIdx >= 0) {
          Object.assign(exec.tasks[tIdx], task)
        }
      }
    }

    if (type === 'metrics.update') {
      metrics.value = payload as PlatformMetrics
    }
  }

  return {
    definitions,
    executions,
    selectedExecution,
    metrics,
    loading,
    error,
    fetchDefinitions,
    createDefinition,
    fetchExecutions,
    fetchExecution,
    triggerWorkflow,
    retryExecution,
    cancelExecution,
    fetchMetrics,
    updateFromWsEvent,
  }
})
