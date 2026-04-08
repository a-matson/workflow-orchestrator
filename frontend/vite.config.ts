import { defineConfig } from 'vite'
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
		sourcemap: true,
		rollupOptions: {
			output: {
				manualChunks: {
					'vue-vendor': ['vue', 'vue-router', 'pinia'],
					vueflow: [
						'@vue-flow/core',
						'@vue-flow/background',
						'@vue-flow/controls',
						'@vue-flow/minimap',
					],
				},
			},
		},
	},
	optimizeDeps: {
		include: ['@vue-flow/core', '@vue-flow/background', '@vue-flow/controls', '@vue-flow/minimap'],
	},
})
