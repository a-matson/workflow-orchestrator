<template>
	<div class="login-page">
		<form class="login-card" data-testid="login-form" @submit.prevent="submit">
			<h1 class="login-title">Sign in to Fluxor</h1>
			<label class="login-label" for="login-key">API key</label>
			<input
				id="login-key"
				v-model="key"
				class="login-input"
				type="password"
				autocomplete="off"
				placeholder="flx_…"
				required
				data-testid="login-key"
			/>
			<p v-if="error" class="login-error" role="alert" data-testid="login-error">{{ error }}</p>
			<button
				class="login-submit"
				type="submit"
				:disabled="busy || !key"
				data-testid="login-submit"
			>
				{{ busy ? 'Signing in…' : 'Sign in' }}
			</button>
		</form>
	</div>
</template>

<script setup lang="ts">
	import { ref } from 'vue'
	import { useRoute, useRouter } from 'vue-router'
	import { login } from '../composables/useSession'

	const route = useRoute()
	const router = useRouter()
	const key = ref('')
	const error = ref('')
	const busy = ref(false)

	// Only an in-app path is followed, so a crafted ?redirect= cannot send a
	// freshly signed-in user to another site; /login itself would strand them here.
	function target(): string {
		const r = route.query.redirect
		const ok =
			typeof r === 'string' && r.startsWith('/') && !r.startsWith('//') && !r.startsWith('/login')
		return ok ? r : '/'
	}

	async function submit() {
		busy.value = true
		error.value = ''
		try {
			await login(key.value.trim())
			key.value = ''
			await router.replace(target())
		} catch (err) {
			error.value = err instanceof Error ? err.message : String(err)
		} finally {
			busy.value = false
		}
	}
</script>

<style scoped>
	.login-page {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.login-card {
		width: 320px;
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 24px;
		background: var(--bg2);
		border: 1px solid var(--border);
		border-radius: var(--r);
	}
	.login-title {
		font-size: 16px;
		font-weight: 700;
		margin: 0 0 6px;
	}
	.login-label {
		font-size: 12px;
		color: var(--text2);
	}
	.login-input {
		padding: 8px 10px;
		font-family: var(--mono);
		font-size: 13px;
		color: var(--text);
		background: var(--bg3);
		border: 1px solid var(--border);
		border-radius: var(--r-sm);
	}
	.login-error {
		margin: 0;
		font-size: 12px;
		color: var(--red);
	}
	.login-submit {
		padding: 8px;
		font-weight: 600;
		color: #fff;
		background: linear-gradient(135deg, #7c6aff, #3b9eff);
		border: 0;
		border-radius: var(--r-sm);
		cursor: pointer;
	}
	.login-submit:disabled {
		opacity: 0.5;
		cursor: default;
	}
</style>
