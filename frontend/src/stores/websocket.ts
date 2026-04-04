import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { WebSocketEvent } from '../types'
import { WS_URL } from '../composables/useApi'
import { useWorkflowStore } from './workflow'

type ConnectionStatus = 'disconnected' | 'connecting' | 'connected' | 'error'

export const useWebSocketStore = defineStore('websocket', () => {
  const status = ref<ConnectionStatus>('disconnected')
  const ws = ref<WebSocket | null>(null)
  const lastEvent = ref<WebSocketEvent | null>(null)
  const eventLog = ref<WebSocketEvent[]>([])
  const reconnectAttempts = ref(0)
  const maxReconnectAttempts = 10
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null

  const isConnected = computed(() => status.value === 'connected')

  function connect(url?: string) {
    const currentUrl = url ?? WS_URL
    if (ws.value?.readyState === WebSocket.OPEN) return

    status.value = 'connecting'

    try {
      const socket = new WebSocket(currentUrl)
      ws.value = socket

      socket.onopen = () => {
        status.value = 'connected'
        reconnectAttempts.value = 0
        ws.value = socket
        console.log('[WS] Connected to', currentUrl)
      }

      socket.onmessage = (event: MessageEvent<string>) => {
        try {
          const data: WebSocketEvent = JSON.parse(event.data)
          lastEvent.value = data
          eventLog.value.unshift(data)
          if (eventLog.value.length > 500) eventLog.value.pop()

          // Route to workflow store
          const workflowStore = useWorkflowStore()
          workflowStore.updateFromWsEvent(data.type, data.payload)
        } catch (err) {
          console.warn('[WS] Failed to parse message:', err)
        }
      }

      socket.onclose = (event) => {
        status.value = 'disconnected'
        ws.value = null
        console.log('[WS] Disconnected:', event.code, event.reason)
        scheduleReconnect(currentUrl)
      }

      socket.onerror = (err) => {
        status.value = 'error'
        console.error('[WS] Error:', err)
        // onclose fires after onerror, which will trigger reconnect
      }
    } catch (err) {
      status.value = 'error'
      console.error('[WS] Failed to create WebSocket:', err)
    }
  }

  function disconnect() {
    if (reconnectTimer) clearTimeout(reconnectTimer)
    ws.value?.close(1000, 'Client disconnect')
    ws.value = null
    status.value = 'disconnected'
    reconnectAttempts.value = 0
  }

  function subscribe(workflowExecId: string) {
    if (!isConnected.value) return
    ws.value?.send(JSON.stringify({ type: 'subscribe', payload: workflowExecId }))
  }

  function scheduleReconnect(url: string) {
    if (reconnectAttempts.value >= maxReconnectAttempts) {
      console.error('[WS] Max reconnect attempts reached')
      return
    }
    const delay = Math.min(1000 * Math.pow(2, reconnectAttempts.value), 30000)
    reconnectAttempts.value++
    console.log(`[WS] Reconnecting in ${delay}ms (attempt ${reconnectAttempts.value})`)
    reconnectTimer = setTimeout(() => connect(url), delay)
  }

  return {
    status,
    isConnected,
    lastEvent,
    eventLog,
    reconnectAttempts,
    connect,
    disconnect,
    subscribe,
  }
})
