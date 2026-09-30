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
	const maxReconnectDelayMs = 30_000
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null
	// onclose fires for our own close() too; without this flag disconnect() would schedule a reconnect.
	let closedByClient = false
	// Events pushed while the socket was down are lost, so the next open must resync.
	let wasDropped = false
	// The server forgets subscriptions with the socket, so they are replayed on every open.
	const subscriptions = new Set<string>()

	const isConnected = computed(() => status.value === 'connected')

	function connect(url?: string) {
		const currentUrl = url ?? WS_URL
		if (ws.value?.readyState === WebSocket.OPEN) return
		closedByClient = false

		status.value = 'connecting'

		try {
			const socket = new WebSocket(currentUrl)
			ws.value = socket

			socket.onopen = () => {
				status.value = 'connected'
				reconnectAttempts.value = 0
				ws.value = socket
				console.log('[WS] Connected to', currentUrl)
				subscriptions.forEach(sendSubscribe)
				if (wasDropped) {
					wasDropped = false
					resync()
				}
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
				if (closedByClient) return
				wasDropped = true
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
		closedByClient = true
		if (reconnectTimer) clearTimeout(reconnectTimer)
		ws.value?.close(1000, 'Client disconnect')
		ws.value = null
		status.value = 'disconnected'
		reconnectAttempts.value = 0
	}

	function sendSubscribe(workflowExecId: string) {
		ws.value?.send(JSON.stringify({ type: 'subscribe', payload: workflowExecId }))
	}

	function subscribe(workflowExecId: string) {
		subscriptions.add(workflowExecId)
		if (!isConnected.value) return
		sendSubscribe(workflowExecId)
	}

	function resync() {
		const workflowStore = useWorkflowStore()
		const selectedId = workflowStore.selectedExecution?.id
		// Failures are recorded in the store's error state; a failed resync must not break the socket.
		void workflowStore.fetchExecutions()
		if (selectedId) void workflowStore.fetchExecution(selectedId).catch(() => {})
	}

	function scheduleReconnect(url: string) {
		// Never gives up: the backend restarts on every deploy and the UI should heal without a reload.
		const ceiling = Math.min(1000 * Math.pow(2, reconnectAttempts.value), maxReconnectDelayMs)
		// Half-jitter keeps a floor so a fleet of tabs does not reconnect in lockstep yet still backs off.
		const delay = Math.round(ceiling / 2 + (Math.random() * ceiling) / 2)
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
