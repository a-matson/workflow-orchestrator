<template>
	<form data-testid="login-form" @submit.prevent>
		<input v-model="key" type="password" data-testid="login-key" />
		<p v-if="error" data-testid="login-error">{{ error }}</p>
		<button type="submit" data-testid="login-submit">Sign in</button>
	</form>
</template>

<script setup lang="ts">
	import { ref } from 'vue'

	// Stub: the login flow lands in the next commit.
	const key = ref('')
	const error = ref('')
</script>
