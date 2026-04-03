import { ref, onUnmounted } from 'vue'
import type { WebSocketEvent } from '../types'

interface UseWebSocketOptions {
  onEvent?: (event: WebSocketEvent) => void
  onConnect?: () => void
  onDisconnect?: () => void
  reconnectDelay?: number
  maxReconnects?: number
}

/**
 * Fine-grained WS composable for component-level subscriptions.
 * The global store (websocket.ts) handles orchestration-level events.
 * This composable is used for targeted log streaming per task/execution.
 */
export function useWebSocket(url: string, opts: UseWebSocketOptions = {}) {
  const connected = ref(false)
  const reconnectCount = ref(0)
  let ws: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | null = null

  function connect() {
    if (ws?.readyState === WebSocket.OPEN) return
    ws = new WebSocket(url)

    ws.onopen = () => {
      connected.value = true
      reconnectCount.value = 0
      opts.onConnect?.()
    }

    ws.onmessage = (e) => {
      const lines = (e.data as string).trim().split('\n')
      for (const line of lines) {
        if (!line) continue
        try {
          const event: WebSocketEvent = JSON.parse(line)
          opts.onEvent?.(event)
        } catch { /* ignore malformed */ }
      }
    }

    ws.onclose = () => {
      connected.value = false
      ws = null
      opts.onDisconnect?.()
      const max = opts.maxReconnects ?? 5
      if (reconnectCount.value < max) {
        const delay = Math.min(1000 * 2 ** reconnectCount.value, 15000)
        reconnectCount.value++
        timer = setTimeout(connect, delay)
      }
    }

    ws.onerror = () => {
      ws?.close()
    }
  }

  function disconnect() {
    if (timer) clearTimeout(timer)
    ws?.close()
    ws = null
    connected.value = false
  }

  function send(data: unknown) {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(data))
    }
  }

  /** Subscribe to a specific workflow execution */
  function subscribeExecution(execId: string) {
    send({ type: 'subscribe', payload: execId })
  }

  onUnmounted(disconnect)

  return { connected, reconnectCount, connect, disconnect, send, subscribeExecution }
}
