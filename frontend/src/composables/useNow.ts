import { ref } from 'vue'

const now = ref(Date.now())
let isTicking = false

export function useNow() {
	if (!isTicking) {
		isTicking = true
		setInterval(() => {
			now.value = Date.now()
		}, 1000)
	}
	return now
}
