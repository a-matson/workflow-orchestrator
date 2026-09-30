import { ref } from 'vue'
import { api } from './useApi'

export type Role = 'viewer' | 'operator' | 'admin'
export interface Principal {
	name: string
	role: Role
}

// Null until GET /api/session succeeds; the router guard and App.vue key off it.
export const principal = ref<Principal | null>(null)

export async function loadSession(): Promise<Principal | null> {
	try {
		principal.value = await api.get<Principal>('/api/session')
	} catch {
		// Any failure, not only 401, leads to the login page, whose submit then
		// shows the real error instead of leaving a blank app.
		principal.value = null
	}
	return principal.value
}

export async function login(apiKey: string): Promise<void> {
	principal.value = await api.post<Principal>('/api/session', { api_key: apiKey })
}

export async function logout(): Promise<void> {
	try {
		await api.delete('/api/session')
	} finally {
		principal.value = null
	}
}
