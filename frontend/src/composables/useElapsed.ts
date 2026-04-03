import { ref, computed, onMounted, onUnmounted } from 'vue'

/**
 * Reactive elapsed-time formatter.
 * Ticks every second and returns a human-readable string.
 */
export function useElapsed(startAt: () => string | undefined, endAt?: () => string | undefined) {
  const now = ref(Date.now())
  let timer: ReturnType<typeof setInterval>

  onMounted(() => { timer = setInterval(() => { now.value = Date.now() }, 1000) })
  onUnmounted(() => clearInterval(timer))

  const ms = computed(() => {
    const start = startAt()
    if (!start) return null
    const end = endAt?.() ? new Date(endAt()!).getTime() : now.value
    return Math.max(0, end - new Date(start).getTime())
  })

  const formatted = computed(() => {
    const v = ms.value
    if (v === null) return '—'
    if (v < 1000) return `${v}ms`
    if (v < 60_000) return `${(v / 1000).toFixed(1)}s`
    const m = Math.floor(v / 60_000)
    const s = Math.floor((v % 60_000) / 1000)
    return `${m}m ${s}s`
  })

  return { ms, formatted }
}

/**
 * Format a Date/string timestamp as HH:MM:SS.mmm
 */
export function formatTimestamp(ts: string): string {
  const d = new Date(ts)
  const pad = (n: number, l = 2) => String(n).padStart(l, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
}
