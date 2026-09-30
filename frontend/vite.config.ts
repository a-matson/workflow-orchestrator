import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
	plugins: [vue()],
	resolve: {
		alias: {
			'@': fileURLToPath(new URL('./src', import.meta.url)),
		},
	},
	server: {
		port: 5173,
		proxy: {
			'/api': {
				target: 'http://localhost:8080',
				changeOrigin: true,
			},
			'/ws': {
				target: 'http://localhost:8080',
				ws: true,
				changeOrigin: true,
			},
		},
	},
	build: {
		outDir: 'dist',
		// Maps would expose original sources to anyone who can reach the UI; nothing consumes them in production.
		sourcemap: false,
		rollupOptions: {
			output: {
				manualChunks: (id) => {
					if (
						id.includes('node_modules/vue') ||
						id.includes('node_modules/vue-router') ||
						id.includes('node_modules/pinia')
					) {
						return 'vue-vendor'
					}
					if (id.includes('node_modules/@vue-flow')) {
						return 'vueflow'
					}
				},
			},
		},
	},
	test: {
		environment: 'happy-dom',
		include: ['src/**/*.spec.ts'],
		coverage: {
			// Count every source file, not only those a test imports: otherwise a new spec that
			// pulls in a large untested component lowers the ratio and trips the ratchet.
			include: ['src/**/*.{ts,vue}'],
			exclude: ['src/**/*.spec.ts', 'src/**/*.d.ts'],
		},
	},
	optimizeDeps: {
		include: ['@vue-flow/core', '@vue-flow/background', '@vue-flow/controls', '@vue-flow/minimap'],
	},
})
