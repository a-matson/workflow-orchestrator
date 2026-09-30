import {
	createRouter,
	createWebHistory,
	type RouteLocationNormalized,
	type Router,
} from 'vue-router'
import BuilderPage from '../pages/Builder.vue'
import ExecutionsPage from '../pages/Executions.vue'
import LogsPage from '../pages/Logs.vue'
import MetricsPage from '../pages/Metrics.vue'
import LoginPage from '../pages/Login.vue'
import { loadSession, principal } from '../composables/useSession'

export const navRoutes = [
	{
		path: '/builder',
		name: 'builder',
		component: BuilderPage,
		meta: { title: 'Builder', icon: '◈' },
	},
	{
		path: '/executions',
		name: 'executions',
		component: ExecutionsPage,
		meta: { title: 'Executions', icon: '⬡' },
	},
	{ path: '/logs', name: 'logs', component: LogsPage, meta: { title: 'Logs', icon: '≡' } },
	{
		path: '/metrics',
		name: 'metrics',
		component: MetricsPage,
		meta: { title: 'Metrics', icon: '◎' },
	},
]

export const router = createRouter({
	history: createWebHistory(),
	routes: [
		{ path: '/', redirect: '/builder' },
		{ path: '/login', name: 'login', component: LoginPage, meta: { public: true } },
		...navRoutes,
		{
			path: '/builder/:workflowId',
			name: 'builder-edit',
			component: BuilderPage,
			meta: { title: 'Builder', icon: '◈' },
		},
		{
			path: '/executions/:execId',
			name: 'execution-detail',
			component: ExecutionsPage,
			meta: { title: 'Executions', icon: '⬡' },
		},
		{
			path: '/logs/:execId',
			name: 'logs-execution',
			component: LogsPage,
			meta: { title: 'Logs', icon: '≡' },
		},
	],
})

router.beforeEach(requireSession)

export async function requireSession(to: RouteLocationNormalized) {
	if (to.meta.public || principal.value || (await loadSession())) return true
	return { name: 'login', query: { redirect: to.fullPath } }
}

// Returns the useApi 401 hook. Every in-flight request fails at once when a
// session expires, so only the first 401 navigates; the rest are dropped.
export function handleExpiredSession(router: Router): () => void {
	let redirecting = false
	return () => {
		const from = router.currentRoute.value
		if (redirecting || from.name === 'login') return
		redirecting = true
		principal.value = null
		router
			.push({ name: 'login', query: { redirect: from.fullPath } })
			.finally(() => (redirecting = false))
	}
}
