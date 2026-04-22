<template>
	<Teleport to="body">
		<div class="modal-backdrop" @click.self="$emit('close')">
			<div class="modal" @dragover.prevent @drop.prevent="onDrop">
				<!-- Header -->
				<div class="modal-header">
					<span class="modal-title">Import YAML Workflow</span>
					<button class="modal-close" @click="$emit('close')">✕</button>
				</div>

				<!-- Body -->
				<div class="modal-body">
					<!-- Format tabs -->
					<div class="format-tabs">
						<button
							v-for="fmt in formats"
							:key="fmt.id"
							class="fmt-tab"
							:class="{ active: activeFormat === fmt.id }"
							@click="activeFormat = fmt.id"
						>
							<span class="fmt-icon">{{ fmt.icon }}</span>
							{{ fmt.label }}
						</button>
					</div>

					<!-- Format description -->
					<div class="fmt-desc">
						<template v-if="activeFormat === 'github'">
							Paste or upload a <code>.github/workflows/*.yml</code> file. Jobs become tasks,
							<code>needs:</code> becomes dependencies, <code>run:</code> scripts map to shell
							tasks, <code>docker://</code> actions map to container tasks. Named actions (e.g.
							<code>actions/checkout@v4</code>) become placeholder shell tasks you can customise.
						</template>
						<template v-else>
							Paste or upload a Fluxor native YAML file. Must have a top-level
							<code>tasks:</code> key. Round-trips perfectly with the DAG builder.
							<a class="fmt-link" href="#" @click.prevent="showNativeExample = !showNativeExample">
								{{ showNativeExample ? 'hide' : 'show' }} example ↓
							</a>
							<pre v-if="showNativeExample" class="example-block">{{ nativeExample }}</pre>
						</template>
					</div>

					<!-- Drop zone / file picker -->
					<div
						class="drop-zone"
						:class="{ dragging: isDragging, 'has-file': !!fileName }"
						@dragenter="isDragging = true"
						@dragleave="isDragging = false"
						@dragover.prevent
						@drop.prevent="onDrop"
						@click="fileInput?.click()"
					>
						<input
							ref="fileInput"
							type="file"
							accept=".yml,.yaml"
							style="display: none"
							@change="onFileChange"
						/>
						<template v-if="fileName">
							<span class="dz-file-icon">📄</span>
							<span class="dz-file-name">{{ fileName }}</span>
							<button class="dz-clear" @click.stop="clearFile">×</button>
						</template>
						<template v-else>
							<span class="dz-icon">⬆</span>
							<span class="dz-label">Drop a .yml file here or click to browse</span>
						</template>
					</div>

					<!-- Divider -->
					<div class="or-divider"><span>or paste YAML</span></div>

					<!-- Paste area -->
					<div class="paste-wrap">
						<div class="paste-line-numbers" aria-hidden="true">
							<span v-for="n in pasteLines" :key="n">{{ n }}</span>
						</div>
						<textarea
							v-model="pastedYaml"
							class="paste-area"
							placeholder="name: My Workflow&#10;jobs:&#10;  build:&#10;    runs-on: ubuntu-latest&#10;    steps:&#10;      - name: Run tests&#10;        run: npm test"
							spellcheck="false"
							@input="clearFile"
						/>
					</div>

					<!-- Error / preview -->
					<div v-if="error" class="import-error">⚠ {{ error }}</div>

					<div v-if="preview" class="preview-panel">
						<div class="preview-header">
							<span class="preview-title">Preview — {{ preview.name }}</span>
							<span class="preview-meta">{{ preview.tasks.length }} tasks</span>
						</div>
						<div class="preview-tasks">
							<div v-for="t in preview.tasks" :key="t.id" class="preview-task">
								<span class="pt-type" :class="t.type">{{ t.type }}</span>
								<span class="pt-name">{{ t.name }}</span>
								<span v-if="t.dependencies?.length" class="pt-deps">
									← {{ t.dependencies.join(', ') }}
								</span>
								<span
									v-if="t.metadata?.gha_action"
									class="pt-note"
									title="Named GitHub Actions cannot run natively — will be a placeholder"
								>
									⚠ action placeholder
								</span>
								<span v-if="t.container" class="pt-container">🐳 {{ t.container.image }}</span>
							</div>
						</div>
					</div>
				</div>

				<!-- Footer -->
				<div class="modal-footer">
					<button class="btn-secondary" @click="$emit('close')">Cancel</button>
					<button class="btn-secondary" :disabled="!hasInput || parsing" @click="parseOnly">
						{{ parsing ? 'Parsing…' : 'Preview' }}
					</button>
					<button class="btn-primary" :disabled="!preview || loading" @click="importAndLoad">
						{{ loading ? 'Loading…' : 'Load into Builder' }}
					</button>
					<button class="btn-save" :disabled="!preview || loading" @click="importAndSave">
						{{ loading ? 'Saving…' : 'Load & Save' }}
					</button>
				</div>
			</div>
		</div>
	</Teleport>
</template>

<script setup lang="ts">
	import { ref, computed } from 'vue'
	import type { WorkflowDefinition } from '../types'

	const emit = defineEmits<{
		(e: 'close'): void
		(e: 'loaded', def: WorkflowDefinition): void
		(e: 'saved', def: WorkflowDefinition): void
	}>()

	// ── State ──────────────────────────────────────────────────────
	const activeFormat = ref<'github' | 'native'>('github')
	const pastedYaml = ref('')
	const fileName = ref('')
	const fileBytes = ref<Uint8Array | null>(null)
	const isDragging = ref(false)
	const parsing = ref(false)
	const loading = ref(false)
	const error = ref('')
	const preview = ref<WorkflowDefinition | null>(null)
	const showNativeExample = ref(false)
	const fileInput = ref<HTMLInputElement | null>(null)

	const formats = [
		{ id: 'github' as const, icon: '⚙', label: 'GitHub Actions' },
		{ id: 'native' as const, icon: '⬡', label: 'Fluxor YAML' },
	]

	const nativeExample = `name: Data Pipeline
description: Fetch, transform, and notify
version: 1.0.0
max_parallel: 4
tasks:
  - id: fetch
    name: Fetch Data
    type: http_request
    config:
      url: https://api.example.com/data
      method: GET
    artifacts_out:
      - path: data.json
    timeout_seconds: 30

  - id: transform
    name: Transform
    type: data_transform
    dependencies: [fetch]
    config:
      script: |
        jq '.items' /workspace/data.json > /workspace/out.json
    artifacts_in:
      - path: data.json
    artifacts_out:
      - path: out.json
    container:
      image: ghcr.io/jqlang/jq:1.7
      memory_mb: 128

  - id: notify
    name: Notify
    type: notification
    dependencies: [transform]
    config:
      notify_type: webhook
      channel: https://webhook.site/your-id
      message: Pipeline complete`

	const pasteLines = computed(() => {
		const n = Math.max(8, (pastedYaml.value || '').split('\n').length)
		return Array.from({ length: n }, (_, i) => i + 1)
	})

	const hasInput = computed(() => !!fileBytes.value || pastedYaml.value.trim().length > 0)

	// ── File handling ──────────────────────────────────────────────
	function onFileChange(e: Event) {
		const f = (e.target as HTMLInputElement).files?.[0]
		if (f) readFile(f)
	}

	function onDrop(e: DragEvent) {
		isDragging.value = false
		const f = e.dataTransfer?.files?.[0]
		if (f) readFile(f)
	}

	function readFile(f: File) {
		fileName.value = f.name
		pastedYaml.value = ''
		preview.value = null
		error.value = ''
		const reader = new FileReader()
		reader.onload = (e) => {
			fileBytes.value = new Uint8Array(e.target!.result as ArrayBuffer)
		}
		reader.readAsArrayBuffer(f)
	}

	function clearFile() {
		fileName.value = ''
		fileBytes.value = null
		if (fileInput.value) fileInput.value.value = ''
	}

	// ── Parsing ────────────────────────────────────────────────────
	async function parseOnly() {
		error.value = ''
		preview.value = null
		parsing.value = true
		try {
			preview.value = await callImportAPI()
		} catch (e: any) {
			error.value = e.message
		} finally {
			parsing.value = false
		}
	}

	async function callImportAPI(): Promise<WorkflowDefinition> {
		let body: BodyInit
		let headers: Record<string, string> = {}

		if (fileBytes.value) {
			const form = new FormData()
			const blob = new Blob([fileBytes.value as BlobPart], { type: 'application/x-yaml' })
			form.append('file', blob, fileName.value || 'workflow.yml')
			body = form
			// Don't set Content-Type — browser sets it with boundary
		} else {
			body = new TextEncoder().encode(pastedYaml.value.trim())
			headers['Content-Type'] = 'application/x-yaml'
			if (fileName.value) {
				headers['Content-Disposition'] = `attachment; filename="${fileName.value}"`
			}
		}

		const res = await fetch('/api/import/yaml', { method: 'POST', headers, body })
		const data = await res.json()
		if (!res.ok) throw new Error(data.error || `Server error ${res.status}`)
		return data as WorkflowDefinition
	}

	async function importAndLoad() {
		error.value = ''
		if (!preview.value) {
			try {
				loading.value = true
				preview.value = await callImportAPI()
			} catch (e: any) {
				error.value = e.message
				loading.value = false
				return
			}
		}
		emit('loaded', preview.value!)
		emit('close')
		loading.value = false
	}

	async function importAndSave() {
		error.value = ''
		loading.value = true
		if (!preview.value) {
			try {
				preview.value = await callImportAPI()
			} catch (e: any) {
				error.value = e.message
				loading.value = false
				return
			}
		}
		emit('saved', preview.value!)
		emit('close')
		loading.value = false
	}
</script>

<style scoped>
	/* ── Backdrop & modal shell ─────────────────────────────────── */
	.modal-backdrop {
		position: fixed;
		inset: 0;
		z-index: 1000;
		background: rgba(0, 0, 0, 0.65);
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 20px;
	}

	.modal {
		width: 680px;
		max-width: 100%;
		max-height: 90vh;
		background: var(--bg2);
		border: 1px solid var(--border);
		border-radius: 10px;
		overflow: hidden;
		display: flex;
		flex-direction: column;
		box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5);
	}

	.modal-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 14px 18px;
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
	}
	.modal-title {
		font-size: 14px;
		font-weight: 700;
		color: var(--text);
	}
	.modal-close {
		background: none;
		border: none;
		color: var(--text3);
		font-size: 16px;
		cursor: pointer;
	}
	.modal-close:hover {
		color: var(--text);
	}

	.modal-body {
		flex: 1;
		overflow-y: auto;
		padding: 18px;
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	/* ── Format tabs ──────────────────────────────────────────────── */
	.format-tabs {
		display: flex;
		gap: 6px;
	}
	.fmt-tab {
		display: flex;
		align-items: center;
		gap: 5px;
		padding: 6px 14px;
		background: var(--surface);
		border: 1px solid var(--border2);
		border-radius: var(--r-sm);
		font-size: 12px;
		color: var(--text2);
		cursor: pointer;
	}
	.fmt-tab:hover {
		background: var(--surface2);
	}
	.fmt-tab.active {
		background: rgba(124, 106, 255, 0.12);
		border-color: var(--accent);
		color: var(--accent);
	}
	.fmt-icon {
		font-size: 13px;
	}

	.fmt-desc {
		font-size: 11px;
		color: var(--text3);
		line-height: 1.6;
		background: rgba(255, 255, 255, 0.02);
		border: 1px solid var(--border);
		border-radius: var(--r-sm);
		padding: 8px 12px;
	}
	.fmt-desc code {
		font-family: var(--mono);
		font-size: 10px;
		background: rgba(255, 255, 255, 0.06);
		padding: 0 3px;
		border-radius: 2px;
		color: var(--text2);
	}
	.fmt-link {
		color: var(--accent);
		text-decoration: none;
		font-size: 10px;
	}
	.fmt-link:hover {
		text-decoration: underline;
	}
	.example-block {
		margin-top: 8px;
		padding: 10px;
		background: #07070e;
		border-radius: 5px;
		font-family: var(--mono);
		font-size: 10px;
		color: #8a9ab5;
		line-height: 1.6;
		white-space: pre;
		overflow-x: auto;
	}

	/* ── Drop zone ────────────────────────────────────────────────── */
	.drop-zone {
		border: 2px dashed var(--border2);
		border-radius: var(--r-sm);
		padding: 20px;
		text-align: center;
		cursor: pointer;
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 8px;
		transition:
			border-color 0.15s,
			background 0.15s;
		min-height: 68px;
	}
	.drop-zone:hover,
	.drop-zone.dragging {
		border-color: var(--accent);
		background: rgba(124, 106, 255, 0.05);
	}
	.drop-zone.has-file {
		background: rgba(34, 211, 160, 0.04);
		border-color: rgba(34, 211, 160, 0.3);
	}
	.dz-icon {
		font-size: 22px;
		color: var(--text3);
	}
	.dz-label {
		font-size: 12px;
		color: var(--text3);
	}
	.dz-file-icon {
		font-size: 20px;
	}
	.dz-file-name {
		font-size: 12px;
		color: var(--green);
		font-family: var(--mono);
	}
	.dz-clear {
		background: none;
		border: none;
		cursor: pointer;
		color: var(--text3);
		font-size: 18px;
	}
	.dz-clear:hover {
		color: var(--red);
	}

	/* ── Divider ──────────────────────────────────────────────────── */
	.or-divider {
		display: flex;
		align-items: center;
		gap: 10px;
		color: var(--text3);
		font-size: 10px;
	}
	.or-divider::before,
	.or-divider::after {
		content: '';
		flex: 1;
		height: 1px;
		background: var(--border);
	}

	/* ── Paste area ───────────────────────────────────────────────── */
	.paste-wrap {
		display: flex;
		border: 1px solid var(--border2);
		border-radius: var(--r-sm);
		overflow: hidden;
		background: #08080f;
	}
	.paste-wrap:focus-within {
		border-color: var(--accent);
	}

	.paste-line-numbers {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		padding: 8px 6px 8px 8px;
		min-width: 32px;
		background: #060610;
		border-right: 1px solid rgba(255, 255, 255, 0.04);
		font-family: var(--mono);
		font-size: 10px;
		line-height: 1.6;
		color: #252535;
		user-select: none;
		overflow: hidden;
		pointer-events: none;
	}
	.paste-line-numbers span {
		display: block;
		height: calc(1.6 * 11px);
	}

	.paste-area {
		flex: 1;
		border: none;
		outline: none;
		resize: none;
		background: transparent;
		padding: 8px 10px;
		font-family: var(--mono);
		font-size: 11px;
		color: #a8b5c8;
		line-height: 1.6;
		min-height: 160px;
	}
	.paste-area::placeholder {
		color: #252535;
	}

	/* ── Error ────────────────────────────────────────────────────── */
	.import-error {
		padding: 8px 12px;
		background: rgba(255, 95, 87, 0.08);
		border: 1px solid rgba(255, 95, 87, 0.25);
		border-radius: var(--r-sm);
		font-size: 11px;
		color: var(--red);
		line-height: 1.5;
		white-space: pre-wrap;
	}

	/* ── Preview ──────────────────────────────────────────────────── */
	.preview-panel {
		border: 1px solid rgba(34, 211, 160, 0.2);
		border-radius: var(--r-sm);
		overflow: hidden;
	}
	.preview-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 7px 12px;
		background: rgba(34, 211, 160, 0.06);
		border-bottom: 1px solid rgba(34, 211, 160, 0.15);
	}
	.preview-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--green);
	}
	.preview-meta {
		font-size: 10px;
		color: var(--text3);
		font-family: var(--mono);
	}

	.preview-tasks {
		padding: 6px 0;
		max-height: 220px;
		overflow-y: auto;
	}
	.preview-task {
		display: flex;
		align-items: center;
		gap: 7px;
		padding: 4px 12px;
		font-size: 11px;
	}
	.preview-task:hover {
		background: rgba(255, 255, 255, 0.02);
	}

	.pt-type {
		font-size: 9px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 3px;
		flex-shrink: 0;
		font-family: var(--mono);
		background: rgba(124, 106, 255, 0.1);
		color: var(--accent);
	}
	.pt-type.http_request {
		background: rgba(59, 158, 255, 0.1);
		color: #63b3ed;
	}
	.pt-type.database_query {
		background: rgba(245, 166, 35, 0.1);
		color: #ecc94b;
	}
	.pt-type.data_transform {
		background: rgba(34, 211, 160, 0.1);
		color: var(--green);
	}
	.pt-type.notification {
		background: rgba(255, 95, 87, 0.1);
		color: var(--red);
	}

	.pt-name {
		flex: 1;
		color: var(--text);
		font-weight: 500;
	}
	.pt-deps {
		font-size: 10px;
		color: var(--text3);
		font-family: var(--mono);
	}
	.pt-note {
		font-size: 9px;
		color: #b8860b;
		background: rgba(184, 134, 11, 0.1);
		padding: 1px 5px;
		border-radius: 3px;
	}
	.pt-container {
		font-size: 9px;
		color: #63b3ed;
		font-family: var(--mono);
	}

	/* ── Footer ───────────────────────────────────────────────────── */
	.modal-footer {
		display: flex;
		align-items: center;
		gap: 8px;
		justify-content: flex-end;
		padding: 12px 18px;
		border-top: 1px solid var(--border);
		flex-shrink: 0;
	}
	.btn-secondary {
		padding: 7px 14px;
		background: var(--surface);
		color: var(--text2);
		border: 1px solid var(--border2);
		border-radius: var(--r-sm);
		font-size: 12px;
	}
	.btn-secondary:hover:not(:disabled) {
		background: var(--surface2);
		color: var(--text);
	}
	.btn-secondary:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}
	.btn-primary {
		padding: 7px 16px;
		background: var(--accent);
		color: #fff;
		border: none;
		border-radius: var(--r-sm);
		font-size: 12px;
		font-weight: 500;
	}
	.btn-primary:hover:not(:disabled) {
		background: #5b4bd4;
	}
	.btn-primary:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.btn-save {
		padding: 7px 16px;
		background: rgba(34, 211, 160, 0.15);
		color: var(--green);
		border: 1px solid rgba(34, 211, 160, 0.3);
		border-radius: var(--r-sm);
		font-size: 12px;
		font-weight: 600;
	}
	.btn-save:hover:not(:disabled) {
		background: rgba(34, 211, 160, 0.25);
	}
	.btn-save:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}
</style>
