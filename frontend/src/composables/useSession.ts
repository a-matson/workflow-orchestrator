import { ref } from 'vue'

export type Role = 'viewer' | 'operator' | 'admin'
export interface Principal {
	name: string
	role: Role
}

// Stub: the browser session lands in the next commit.
export const principal = ref<Principal | null>(null)
export async function loadSession(): Promise<Principal | null> {
	return null
}
export async function login(_apiKey: string): Promise<void> {}
export async function logout(): Promise<void> {}
