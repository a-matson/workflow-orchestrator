const BASE_URL = import.meta.env.VITE_API_URL || 'http://localhost:8080'

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `HTTP ${res.status}`)
  }
  return res.json()
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  delete: <T>(path: string) => request<T>('DELETE', path),
}

export function wsUrl(path = '/ws'): string {
  const base = (import.meta.env.VITE_API_URL || 'http://localhost:8080').replace(/^http/, 'ws')
  return `${base}${path}`
}

// Named alias used by App.vue
export const WS_URL = wsUrl('/ws')
