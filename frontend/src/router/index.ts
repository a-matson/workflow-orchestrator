import { createRouter, createWebHistory } from 'vue-router'
import BuilderPage from '../pages/Builder.vue'
import ExecutionsPage from '../pages/Executions.vue'
import LogsPage from '../pages/Logs.vue'
import MetricsPage from '../pages/Metrics.vue'

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
