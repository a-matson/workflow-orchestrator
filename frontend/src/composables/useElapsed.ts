import { computed } from 'vue'
import { useNow } from './useNow'

export function useElapsed(startAt: () => string | undefined, endAt?: () => string | undefined) {
	const now = useNow()

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
