const BASE_URL = import.meta.env.VITE_API_URL || ''

let onUnauthorized: (() => void) | null = null

// fn runs on every 401 outside /api/session, whose own 401s mean "not signed in"
// or "wrong key" and are handled by the guard and the login page.
export function setUnauthorizedHandler(fn: () => void) {
	onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	const res = await fetch(`${BASE_URL}${path}`, {
		method,
		headers: { 'Content-Type': 'application/json' },
		body: body !== undefined ? JSON.stringify(body) : undefined,
	})
	if (res.status === 401 && !path.startsWith('/api/session')) onUnauthorized?.()
	if (!res.ok) {
		const err = await res.json().catch(() => ({ error: res.statusText }))
		throw new Error((err as { error?: string }).error || `HTTP ${res.status}`)
	}
	if (res.status === 204) return undefined as T
	return res.json()
}

export const api = {
	get: <T>(path: string) => request<T>('GET', path),
	post: <T>(path: string, body: unknown) => request<T>('POST', path, body),
	put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
	delete: <T>(path: string) => request<T>('DELETE', path),
}

export function wsUrl(path = '/ws'): string {
	const base = BASE_URL
		? // Leave the trailing "s" in place so https becomes wss instead of downgrading to ws.
			BASE_URL.replace(/^http/, 'ws')
		: `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}`
	return `${base}${path}`
}

// Named alias used by App.vue
export const WS_URL = wsUrl()
