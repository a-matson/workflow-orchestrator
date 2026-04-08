<template>
  <div class="task-config-fields">
    <!-- HTTP Request -->
    <template v-if="type === 'http_request'">
      <div class="field">
        <label>URL</label>
        <input
          class="cf-input"
          type="url"
          :value="cfg.url as string"
          placeholder="https://api.example.com/endpoint"
          @input="patch('url', ($event.target as HTMLInputElement).value)"
        />
      </div>
      <div class="field">
        <label>Method</label>
        <select
          class="cf-input"
          :value="cfg.method as string"
          @change="patch('method', ($event.target as HTMLSelectElement).value)"
        >
          <option
            v-for="m in ['GET', 'POST', 'PUT', 'PATCH', 'DELETE']"
            :key="m"
            :value="m"
          >
            {{ m }}
          </option>
        </select>
      </div>
      <div class="field">
        <label>Request Body (JSON)</label>
        <textarea
          class="cf-input cf-textarea"
          :value="bodyStr"
          placeholder="{&quot;key&quot;: &quot;value&quot;}"
          @input="patchJsonBody(($event.target as HTMLTextAreaElement).value)"
        ></textarea>
      </div>
      <div class="field">
        <label>Timeout (ms)</label>
        <input
          class="cf-input"
          type="number"
          min="0"
          :value="(cfg.timeout_ms as number) ?? 10000"
          @change="patch('timeout_ms', +($event.target as HTMLInputElement).value)"
        />
      </div>
    </template>

    <!-- Database Query -->
    <template v-else-if="type === 'database_query'">
      <div class="field">
        <label>Connection String</label>
        <input
          class="cf-input"
          type="text"
          :value="cfg.connection_string as string"
          placeholder="postgres://user:pass@host:5432/db"
          @input="patch('connection_string', ($event.target as HTMLInputElement).value)"
        />
      </div>
      <div class="field">
        <label>SQL Query</label>
        <textarea
          class="cf-input cf-textarea cf-code"
          :value="cfg.query as string"
          placeholder="SELECT * FROM events WHERE created_at >= $1"
          rows="4"
          @input="patch('query', ($event.target as HTMLTextAreaElement).value)"
        ></textarea>
      </div>
      <div class="field">
        <label>Max Rows</label>
        <input
          class="cf-input"
          type="number"
          min="1"
          :value="(cfg.max_rows as number) ?? 10000"
          @change="patch('max_rows', +($event.target as HTMLInputElement).value)"
        />
      </div>
    </template>

    <!-- Data Transform -->
    <template v-else-if="type === 'data_transform'">
      <div class="field">
        <label>Input Format</label>
        <select
          class="cf-input"
          :value="(cfg.input_format as string) ?? 'json'"
          @change="patch('input_format', ($event.target as HTMLSelectElement).value)"
        >
          <option
            v-for="f in ['json', 'csv', 'parquet', 'avro']"
            :key="f"
            :value="f"
          >
            {{ f }}
          </option>
        </select>
      </div>
      <div class="field">
        <label>Output Format</label>
        <select
          class="cf-input"
          :value="(cfg.output_format as string) ?? 'json'"
          @change="patch('output_format', ($event.target as HTMLSelectElement).value)"
        >
          <option
            v-for="f in ['json', 'csv', 'parquet', 'avro']"
            :key="f"
            :value="f"
          >
            {{ f }}
          </option>
        </select>
      </div>
      <div class="field">
        <label>Transform Script</label>
        <textarea
          class="cf-input cf-textarea cf-code"
          :value="cfg.script as string"
          placeholder="# Python / jq expression&#10;records.filter(r => r.active)"
          rows="5"
          @input="patch('script', ($event.target as HTMLTextAreaElement).value)"
        ></textarea>
      </div>
    </template>

    <!-- ML Inference -->
    <template v-else-if="type === 'ml_inference'">
      <div class="field">
        <label>Model Name / Path</label>
        <input
          class="cf-input"
          type="text"
          :value="cfg.model_name as string"
          placeholder="my-model-v2 or /models/bert.pt"
          @input="patch('model_name', ($event.target as HTMLInputElement).value)"
        />
      </div>
      <div class="field">
        <label>Input Path</label>
        <input
          class="cf-input"
          type="text"
          :value="cfg.input_path as string"
          placeholder="s3://bucket/input.json"
          @input="patch('input_path', ($event.target as HTMLInputElement).value)"
        />
      </div>
      <div class="field">
        <label>Output Path</label>
        <input
          class="cf-input"
          type="text"
          :value="cfg.output_path as string"
          placeholder="s3://bucket/output.json"
          @input="patch('output_path', ($event.target as HTMLInputElement).value)"
        />
      </div>
      <div class="field">
        <label>Batch Size</label>
        <input
          class="cf-input"
          type="number"
          min="1"
          :value="(cfg.batch_size as number) ?? 32"
          @change="patch('batch_size', +($event.target as HTMLInputElement).value)"
        />
      </div>
    </template>

    <!-- Notification -->
    <template v-else-if="type === 'notification'">
      <div class="field">
        <label>Channel</label>
        <input
          class="cf-input"
          type="text"
          :value="cfg.channel as string"
          placeholder="#team-alerts or user@example.com"
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
        ></textarea>
      </div>
      <div class="field">
        <label>Notification Type</label>
        <select
          class="cf-input"
          :value="(cfg.notify_type as string) ?? 'slack'"
          @change="patch('notify_type', ($event.target as HTMLSelectElement).value)"
        >
          <option
            v-for="t in ['slack', 'email', 'webhook', 'pagerduty']"
            :key="t"
            :value="t"
          >
            {{ t }}
          </option>
        </select>
      </div>
    </template>

    <!-- Generic / default -->
    <template v-else>
      <div class="field">
        <label>Command</label>
        <input
          class="cf-input cf-code"
          type="text"
          :value="cfg.command as string"
          placeholder="bash / python / node"
          @input="patch('command', ($event.target as HTMLInputElement).value)"
        />
      </div>
      <div class="field">
        <label>Arguments (one per line)</label>
        <textarea
          class="cf-input cf-textarea cf-code"
          :value="argsStr"
          placeholder="-c&#10;echo hello"
          rows="3"
          @input="patchArgs(($event.target as HTMLTextAreaElement).value)"
        ></textarea>
      </div>
      <div class="field">
        <label>Environment Variables (KEY=VALUE, one per line)</label>
        <textarea
          class="cf-input cf-textarea cf-code"
          :value="envStr"
          placeholder="MY_VAR=value&#10;OTHER=123"
          rows="3"
          @input="patchEnv(($event.target as HTMLTextAreaElement).value)"
        ></textarea>
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

// HTTP body as JSON string
const bodyStr = computed(() => {
  const b = cfg.value.body
  if (!b) return ''
  if (typeof b === 'string') return b
  return JSON.stringify(b, null, 2)
})
function patchJsonBody(val: string) {
  try {
    patch('body', JSON.parse(val))
  } catch {
    patch('body', val)
  }
}

// Generic command args as newline-separated
const argsStr = computed(() => {
  const a = cfg.value.args
  if (!a) return ''
  if (Array.isArray(a)) return a.join('\n')
  return String(a)
})
function patchArgs(val: string) {
  patch(
    'args',
    val
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean),
  )
}

// Env vars as KEY=VALUE lines
const envStr = computed(() => {
  const e = cfg.value.env
  if (!e || typeof e !== 'object') return ''
  return Object.entries(e as Record<string, string>)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
})
function patchEnv(val: string) {
  const env: Record<string, string> = {}
  for (const line of val.split('\n')) {
    const idx = line.indexOf('=')
    if (idx > 0) env[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
  }
  patch('env', env)
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
.field label {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text3);
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
}
.cf-input:focus {
  border-color: var(--accent);
}
.cf-textarea {
  resize: vertical;
  min-height: 60px;
  line-height: 1.5;
}
.cf-code {
  font-family: var(--mono);
  font-size: 11px;
}
</style>
