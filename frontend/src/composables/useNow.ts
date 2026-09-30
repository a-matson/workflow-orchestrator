import { getCurrentScope, onScopeDispose, ref } from 'vue'

const now = ref(Date.now())
let subscribers = 0
let timer: ReturnType<typeof setInterval> | undefined

// Shared 1s clock. The interval lives only while a component uses it, and `now` is
// refreshed on start because it would otherwise be stale from module load.
export function useNow() {
	if (subscribers++ === 0) {
		now.value = Date.now()
		timer = setInterval(() => {
			now.value = Date.now()
		}, 1000)
	}
	if (getCurrentScope()) {
		onScopeDispose(() => {
			if (--subscribers === 0) clearInterval(timer)
		})
	}
	return now
}
