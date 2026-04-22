<template>
	<div class="task-config-fields">
		<!-- HTTP Request -->
		<template v-if="type === 'http_request'">
			<div class="field-row">
				<div class="field" style="flex: 0 0 90px">
					<label>Method</label>
					<select
						class="cf-input"
						:value="(cfg.method as string) ?? 'GET'"
						@change="patch('method', ($event.target as HTMLSelectElement).value)"
					>
						<option v-for="m in ['GET', 'POST', 'PUT', 'PATCH', 'DELETE']" :key="m" :value="m">
							{{ m }}
						</option>
					</select>
				</div>
				<div class="field" style="flex: 1">
					<label>URL</label>
					<input
						class="cf-input"
						type="url"
						:value="cfg.url as string"
						placeholder="https://api.example.com/endpoint"
						@input="patch('url', ($event.target as HTMLInputElement).value)"
					/>
				</div>
			</div>
			<div class="field">
				<label>Headers <span class="label-hint">KEY: value, one per line</span></label>
				<textarea
					class="cf-input cf-textarea cf-code"
					:value="headersStr"
					placeholder="Authorization: Bearer token&#10;Content-Type: application/json"
					rows="3"
					@input="patchHeaders(($event.target as HTMLTextAreaElement).value)"
				/>
			</div>
			<div class="field">
				<label>Request Body <span class="label-hint">JSON</span></label>
				<div class="code-editor-wrap">
					<div class="code-line-numbers" aria-hidden="true">
						<span v-for="n in bodyLines" :key="n">{{ n }}</span>
					</div>
					<textarea
						class="cf-input cf-textarea cf-code code-editor-ta"
						:value="bodyStr"
						placeholder='{"key": "value"}'
						rows="4"
						spellcheck="false"
						@input="patchJsonBody(($event.target as HTMLTextAreaElement).value)"
						@scroll="syncScroll($event, 'body')"
					/>
				</div>
			</div>
			<div class="field-row">
				<div class="field" style="flex: 1">
					<label>Timeout (ms)</label>
					<input
						class="cf-input"
						type="number"
						min="0"
						:value="(cfg.timeout_ms as number) ?? 10000"
						@change="patch('timeout_ms', +($event.target as HTMLInputElement).value)"
					/>
				</div>
			</div>
		</template>

		<!-- ── Database Query ────────────────────────────────────────── -->
		<template v-else-if="type === 'database_query'">
			<div class="field">
				<label>Connection String</label>
				<input
					class="cf-input cf-code"
					type="text"
					:value="cfg.connection_string as string"
					placeholder="postgres://user:pass@host:5432/db"
					@input="patch('connection_string', ($event.target as HTMLInputElement).value)"
				/>
			</div>
			<div class="field">
				<label>SQL Query</label>
				<div class="code-editor-wrap">
					<div class="code-line-numbers" aria-hidden="true">
						<span v-for="n in sqlLines" :key="n">{{ n }}</span>
					</div>
					<textarea
						class="cf-input cf-textarea cf-code code-editor-ta"
						:value="cfg.query as string"
						placeholder="SELECT *&#10;FROM events&#10;WHERE created_at >= NOW() - INTERVAL '1 day'"
						rows="6"
						spellcheck="false"
						@input="patch('query', ($event.target as HTMLTextAreaElement).value)"
					/>
				</div>
			</div>
			<div class="field-row">
				<div class="field" style="flex: 1">
					<label>Max Rows</label>
					<input
						class="cf-input"
						type="number"
						min="1"
						:value="(cfg.max_rows as number) ?? 10000"
						@change="patch('max_rows', +($event.target as HTMLInputElement).value)"
					/>
				</div>
			</div>
		</template>

		<!-- ── Data Transform ────────────────────────────────────────── -->
		<template v-else-if="type === 'data_transform'">
			<div class="field-row">
				<div class="field" style="flex: 1">
					<label>Input Format</label>
					<select
						class="cf-input"
						:value="(cfg.input_format as string) ?? 'json'"
						@change="patch('input_format', ($event.target as HTMLSelectElement).value)"
					>
						<option v-for="f in ['json', 'csv', 'parquet', 'avro', 'text']" :key="f" :value="f">
							{{ f }}
						</option>
					</select>
				</div>
				<div class="field" style="flex: 1">
					<label>Output Format</label>
					<select
						class="cf-input"
						:value="(cfg.output_format as string) ?? 'json'"
						@change="patch('output_format', ($event.target as HTMLSelectElement).value)"
					>
						<option v-for="f in ['json', 'csv', 'parquet', 'avro', 'text']" :key="f" :value="f">
							{{ f }}
						</option>
					</select>
				</div>
			</div>
			<div class="field">
				<div class="code-editor-header">
					<label>Transform Script</label>
					<div class="editor-actions">
						<span class="editor-hint">shell command — reads stdin, writes stdout</span>
					</div>
				</div>
				<div class="code-editor-wrap">
					<div class="code-line-numbers" aria-hidden="true">
						<span v-for="n in scriptLines" :key="n">{{ n }}</span>
					</div>
					<textarea
						class="cf-input cf-textarea cf-code code-editor-ta"
						:value="cfg.script as string"
						placeholder="jq '. | map(select(.active))' /workspace/input.json > /workspace/output.json"
						rows="7"
						spellcheck="false"
						@input="patch('script', ($event.target as HTMLTextAreaElement).value)"
					/>
				</div>
				<div class="editor-footer">
					<span class="footer-hint"
						>💡 Files from artifact inputs are available at
						<code>/workspace/&lt;path&gt;</code></span
					>
				</div>
			</div>
		</template>

		<!-- ── ML Inference ──────────────────────────────────────────── -->
		<template v-else-if="type === 'ml_inference'">
			<div class="field">
				<label>Model Binary / Script</label>
				<input
					class="cf-input cf-code"
					type="text"
					:value="cfg.model_name as string"
					placeholder="/models/predict.py or my-model-binary"
					@input="patch('model_name', ($event.target as HTMLInputElement).value)"
				/>
			</div>
			<div class="field-row">
				<div class="field" style="flex: 1">
					<label>Input Path</label>
					<input
						class="cf-input cf-code"
						type="text"
						:value="cfg.input_path as string"
						placeholder="/workspace/input.json"
						@input="patch('input_path', ($event.target as HTMLInputElement).value)"
					/>
				</div>
				<div class="field" style="flex: 1">
					<label>Output Path</label>
					<input
						class="cf-input cf-code"
						type="text"
						:value="cfg.output_path as string"
						placeholder="/workspace/output.json"
						@input="patch('output_path', ($event.target as HTMLInputElement).value)"
					/>
				</div>
			</div>
			<div class="field-row">
				<div class="field" style="flex: 1">
					<label>Batch Size</label>
					<input
						class="cf-input"
						type="number"
						min="1"
						:value="(cfg.batch_size as number) ?? 32"
						@change="patch('batch_size', +($event.target as HTMLInputElement).value)"
					/>
				</div>
			</div>
		</template>

		<!-- ── Notification ──────────────────────────────────────────── -->
		<template v-else-if="type === 'notification'">
			<div class="field">
				<label>Notification Type</label>
				<div class="notify-type-row">
					<button
						v-for="t in ['slack', 'webhook', 'email', 'pagerduty']"
						:key="t"
						class="notify-type-btn"
						:class="{ active: (cfg.notify_type ?? 'slack') === t }"
						@click="patch('notify_type', t)"
					>
						<span class="notify-icon">{{ notifyIcon(t) }}</span>
						{{ t }}
					</button>
				</div>
			</div>
			<div class="field">
				<label>{{ notifyChannelLabel }}</label>
				<input
					class="cf-input"
					type="text"
					:value="cfg.channel as string"
					:placeholder="notifyChannelPlaceholder"
					@input="patch('channel', ($event.target as HTMLInputElement).value)"
				/>
			</div>
			<div class="field">
				<label>Message</label>
				<textarea
					class="cf-input cf-textarea"
					:value="cfg.message as string"
					placeholder="Workflow completed successfully"
					rows="3"
					@input="patch('message', ($event.target as HTMLTextAreaElement).value)"
				/>
			</div>
		</template>

		<!-- ── Generic / Shell ───────────────────────────────────────── -->
		<template v-else>
			<div class="field">
				<div class="code-editor-header">
					<label>Command</label>
					<span class="editor-hint">binary name or full path</span>
				</div>
				<input
					class="cf-input cf-code"
					type="text"
					:value="cfg.command as string"
					placeholder="bash  /  python3  /  node  /  /usr/local/bin/my-tool"
					@input="patch('command', ($event.target as HTMLInputElement).value)"
				/>
			</div>
			<div class="field">
				<div class="code-editor-header">
					<label>Arguments</label>
					<span class="editor-hint">one per line → passed as separate argv entries</span>
				</div>
				<div class="code-editor-wrap">
					<div class="code-line-numbers" aria-hidden="true">
						<span v-for="n in argsLines" :key="n">{{ n }}</span>
					</div>
					<textarea
						class="cf-input cf-textarea cf-code code-editor-ta"
						:value="argsStr"
						placeholder="-c&#10;echo 'hello from fluxor'"
						rows="4"
						spellcheck="false"
						@input="patchArgs(($event.target as HTMLTextAreaElement).value)"
					/>
				</div>
			</div>
			<div class="field">
				<div class="code-editor-header">
					<label>Environment Variables</label>
					<span class="editor-hint">KEY=value, one per line</span>
				</div>
				<div class="code-editor-wrap">
					<div class="code-line-numbers" aria-hidden="true">
						<span v-for="n in envLines" :key="n">{{ n }}</span>
					</div>
					<textarea
						class="cf-input cf-textarea cf-code code-editor-ta"
						:value="envStr"
						placeholder="MY_VAR=hello&#10;DEBUG=true"
						rows="3"
						spellcheck="false"
						@input="patchEnv(($event.target as HTMLTextAreaElement).value)"
					/>
				</div>
			</div>
			<div class="editor-footer">
				<span class="footer-hint"
					>💡 Workspace artifacts available at <code>/workspace/&lt;path&gt;</code></span
				>
			</div>
		</template>
	</div>
</template>

<script setup lang="ts">
	import { computed } from 'vue'

	const props = defineProps<{
		type: string
		config: Record<string, unknown>
	}>()

	const emit = defineEmits<{
		(e: 'update', config: Record<string, unknown>): void
	}>()

	const cfg = computed(() => props.config)

	function patch(key: string, value: unknown) {
		emit('update', { ...props.config, [key]: value })
	}

	// ── HTTP body ──────────────────────────────────────────────────────────
	const bodyStr = computed(() => {
		const b = cfg.value.body
		if (!b) return ''
		if (typeof b === 'string') return b
		return JSON.stringify(b, null, 2)
	})
	const bodyLines = computed(() => lineCount(bodyStr.value, 4))

	function patchJsonBody(val: string) {
		try {
			patch('body', JSON.parse(val))
		} catch {
			patch('body', val)
		}
	}

	// ── HTTP headers ───────────────────────────────────────────────────────
	const headersStr = computed(() => {
		const h = cfg.value.headers
		if (!h || typeof h !== 'object') return ''
		return Object.entries(h as Record<string, string>)
			.map(([k, v]) => `${k}: ${v}`)
			.join('\n')
	})
	function patchHeaders(val: string) {
		const headers: Record<string, string> = {}
		for (const line of val.split('\n')) {
			const idx = line.indexOf(':')
			if (idx > 0) headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
		}
		patch('headers', headers)
	}

	// ── SQL lines ──────────────────────────────────────────────────────────
	const sqlLines = computed(() => lineCount(cfg.value.query as string, 6))

	// ── Script lines ───────────────────────────────────────────────────────
	const scriptLines = computed(() => lineCount(cfg.value.script as string, 7))

	// ── Args ───────────────────────────────────────────────────────────────
	const argsStr = computed(() => {
		const a = cfg.value.args
		if (!a) return ''
		if (Array.isArray(a)) return a.join('\n')
		return String(a)
	})
	const argsLines = computed(() => lineCount(argsStr.value, 4))
	function patchArgs(val: string) {
		patch(
			'args',
			val
				.split('\n')
				.map((s) => s.trim())
				.filter(Boolean),
		)
	}

	// ── Env vars ───────────────────────────────────────────────────────────
	const envStr = computed(() => {
		const e = cfg.value.env
		if (!e || typeof e !== 'object') return ''
		return Object.entries(e as Record<string, string>)
			.map(([k, v]) => `${k}=${v}`)
			.join('\n')
	})
	const envLines = computed(() => lineCount(envStr.value, 3))
	function patchEnv(val: string) {
		const env: Record<string, string> = {}
		for (const line of val.split('\n')) {
			const idx = line.indexOf('=')
			if (idx > 0) env[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
		}
		patch('env', env)
	}

	// ── Notification helpers ───────────────────────────────────────────────
	const notifyChannelLabel = computed(() => {
		switch (cfg.value.notify_type ?? 'slack') {
			case 'slack':
				return 'Slack Webhook URL'
			case 'email':
				return 'To (email address)'
			case 'pagerduty':
				return 'PagerDuty Routing Key'
			default:
				return 'Webhook URL'
		}
	})
	const notifyChannelPlaceholder = computed(() => {
		switch (cfg.value.notify_type ?? 'slack') {
			case 'slack':
				return 'https://hooks.slack.com/services/…'
			case 'email':
				return 'team@example.com'
			case 'pagerduty':
				return 'R01AB2CDEFG…'
			default:
				return 'https://webhook.site/your-uuid'
		}
	})
	function notifyIcon(t: string) {
		return { slack: '💬', webhook: '🔗', email: '✉', pagerduty: '🚨' }[t] ?? '📣'
	}

	// ── Shared helpers ─────────────────────────────────────────────────────
	function lineCount(text: string | undefined, min: number): number[] {
		const n = Math.max(min, (text ?? '').split('\n').length)
		return Array.from({ length: n }, (_, i) => i + 1)
	}

	function syncScroll(e: Event, _area: string) {
		const ta = e.target as HTMLTextAreaElement
		const ln = ta.previousElementSibling as HTMLElement
		if (ln) ln.scrollTop = ta.scrollTop
	}
</script>

<style scoped>
	.task-config-fields {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.field-row {
		display: flex;
		gap: 8px;
	}
	.field-row .field {
		flex: 1;
	}

	.field label,
	.code-editor-header label {
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--text3);
	}
	.label-hint {
		text-transform: none;
		letter-spacing: 0;
		font-weight: 400;
		color: #444;
		margin-left: 4px;
	}

	.cf-input {
		font-size: 12px;
		background: var(--surface);
		border: 1px solid var(--border2);
		color: var(--text);
		border-radius: var(--r-sm);
		padding: 6px 9px;
		outline: none;
		width: 100%;
		box-sizing: border-box;
	}
	.cf-input:focus {
		border-color: var(--accent);
	}
	.cf-textarea {
		resize: vertical;
		min-height: 56px;
		line-height: 1.6;
	}
	.cf-code {
		font-family: var(--mono);
		font-size: 11px;
	}

	/* ── Code editor widget ──────────────────────────────────── */
	.code-editor-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 4px;
	}
	.editor-actions {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.editor-hint {
		font-size: 9px;
		color: #333;
		font-style: italic;
	}

	.code-editor-wrap {
		display: flex;
		border: 1px solid var(--border2);
		border-radius: var(--r-sm);
		overflow: hidden;
		background: #0a0a14;
	}
	.code-editor-wrap:focus-within {
		border-color: var(--accent);
	}

	.code-line-numbers {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		padding: 6px 6px 6px 8px;
		min-width: 30px;
		background: #080810;
		border-right: 1px solid rgba(255, 255, 255, 0.05);
		font-family: var(--mono);
		font-size: 10px;
		line-height: 1.6;
		color: #2a2a44;
		user-select: none;
		overflow: hidden;
		pointer-events: none;
	}
	.code-line-numbers span {
		display: block;
		height: calc(1.6 * 11px);
	}

	.code-editor-ta {
		flex: 1;
		border: none !important;
		border-radius: 0 !important;
		background: transparent !important;
		resize: none;
		padding: 6px 8px;
		line-height: 1.6;
		outline: none;
		overflow: auto;
	}
	.code-editor-ta:focus {
		border-color: transparent !important;
	}

	.editor-footer {
		margin-top: 3px;
	}
	.footer-hint {
		font-size: 9.5px;
		color: #333;
	}
	.footer-hint code {
		font-family: var(--mono);
		background: rgba(255, 255, 255, 0.04);
		padding: 0 3px;
		border-radius: 2px;
		color: #666;
	}

	/* ── Notification type selector ──────────────────────────── */
	.notify-type-row {
		display: flex;
		gap: 5px;
		flex-wrap: wrap;
	}
	.notify-type-btn {
		display: flex;
		align-items: center;
		gap: 4px;
		padding: 4px 10px;
		background: var(--surface);
		border: 1px solid var(--border2);
		border-radius: var(--r-sm);
		font-size: 11px;
		color: var(--text2);
		cursor: pointer;
	}
	.notify-type-btn:hover {
		background: var(--surface2);
	}
	.notify-type-btn.active {
		background: rgba(124, 106, 255, 0.12);
		border-color: rgba(124, 106, 255, 0.4);
		color: var(--accent);
	}
	.notify-icon {
		font-size: 12px;
	}
</style>
