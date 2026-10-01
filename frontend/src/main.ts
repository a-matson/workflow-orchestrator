import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { handleExpiredSession, router } from './router'
import { setUnauthorizedHandler } from './composables/useApi'
import { installErrorReporting } from './composables/useErrorReporting'
import App from './App.vue'
import './assets/main.css'

setUnauthorizedHandler(handleExpiredSession(router))

const app = createApp(App)
installErrorReporting(app)
app.use(createPinia())
app.use(router)
app.mount('#app')
