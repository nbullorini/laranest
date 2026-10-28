<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { AnsiUp } from 'ansi_up'
import { NAutoComplete, NButton, NIcon, NInputGroup, NInputGroupLabel, NSpin, NTag, useMessage } from 'naive-ui'
import { ChevronDown, ChevronForward, Play, Stop } from '@vicons/ionicons5'
import { api, type Command } from '../api'

const props = defineProps<{ appId: string }>()
const emit = defineEmits<{ finished: [] }>()
const message = useMessage()

const commands = ref<Command[]>([])
const outputs = ref<Record<string, string>>({})
const open = ref<string | null>(null)
const line = ref('')
const starting = ref(false)
const outBox = ref<HTMLElement | null>(null)
const ansi = new AnsiUp() // escapes HTML before adding color spans
let timer: number | undefined

const common = ['php artisan migrate', 'php artisan optimize:clear', 'php artisan storage:link', 'composer install', 'npm install', 'npm run build']
const suggestions = computed(() => {
  const typed = line.value.trim().toLowerCase()
  const seen = new Set<string>()
  return [...commands.value.map((c) => c.line), ...common]
    .filter((l) => !seen.has(l) && seen.add(l) && l.toLowerCase().includes(typed) && l !== line.value)
    .slice(0, 8)
    .map((l) => ({ label: l, value: l }))
})
const running = computed(() => commands.value.some((c) => c.status === 'running'))

async function load() {
  try {
    const before = new Set(commands.value.filter((c) => c.status === 'running').map((c) => c.id))
    commands.value = await api.commands(props.appId)
    if (commands.value.some((c) => before.has(c.id) && c.status !== 'running')) emit('finished')
    if (open.value) await loadOutput(open.value)
  } catch (e) {
    message.error((e as Error).message)
  }
}

async function loadOutput(id: string) {
  const box = outBox.value
  const atEnd = !box || box.scrollHeight - box.scrollTop - box.clientHeight < 40
  const c = await api.command(props.appId, id)
  outputs.value[id] = ansi.ansi_to_html(c.output || '')
  if (atEnd) nextTick(() => outBox.value?.scrollTo({ top: outBox.value.scrollHeight }))
}

async function toggle(id: string) {
  open.value = open.value === id ? null : id
  if (open.value) await loadOutput(open.value)
}

/** Runs `laranest <text>` in the app's folder and shows its output. */
async function run(text = line.value, fixes = '') {
  if (!text.trim()) return
  starting.value = true
  try {
    const c = await api.runCommand(props.appId, text, fixes)
    line.value = ''
    open.value = c.id
    outputs.value[c.id] = ''
    await load()
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    starting.value = false
  }
}

async function stop(id: string) {
  await api.stopCommand(props.appId, id).catch((e) => message.error((e as Error).message))
  await load()
}

function when(c: Command) {
  const start = new Date(c.started_at)
  const end = c.finished_at ? new Date(c.finished_at) : new Date()
  const secs = Math.max(0, Math.round((end.getTime() - start.getTime()) / 1000))
  const took = secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${secs % 60}s`
  return `${start.toLocaleString()} · ${took}`
}

const tagType = (c: Command) => c.unresolved ? 'warning' : ({ running: 'info', ok: 'success', failed: 'error', stopped: 'warning' } as const)[c.status]
function tagText(c: Command) {
  if (c.unresolved) return 'ok · warning remains'
  return c.status === 'failed' && c.exit_code !== undefined ? `exit ${c.exit_code}` : c.status
}

// Poll while something runs (the output grows) and slowly otherwise.
function schedule() {
  clearTimeout(timer)
  timer = window.setTimeout(async () => { await load(); schedule() }, running.value ? 1000 : 5000)
}
watch(() => props.appId, async () => { open.value = null; outputs.value = {}; await load(); schedule() }, { immediate: true })
onBeforeUnmount(() => clearTimeout(timer))

defineExpose({ run })
</script>

<template>
  <div class="stack" style="gap: 12px">
    <NInputGroup>
      <NInputGroupLabel class="mono">laranest</NInputGroupLabel>
      <NAutoComplete v-model:value="line" :options="suggestions" placeholder="php artisan migrate" class="mono"
        :input-props="{ spellcheck: false, autocomplete: 'off' }" @keydown.enter.prevent="run()" />
      <NButton type="primary" :loading="starting" :disabled="!line.trim()" @click="run()">
        <template #icon><NIcon :component="Play" /></template>Run
      </NButton>
    </NInputGroup>
    <small class="muted">
      Runs in the project's folder with its PHP and Node, like <code>laranest …</code> in a terminal there:
      php, composer, npm, npx and node. Commands can't ask questions (no keyboard input).
    </small>

    <div v-if="!commands.length" class="muted">No commands yet. Commands run here, or with a warning's Run button, show up here.</div>
    <div v-for="c in commands" :key="c.id" class="cmd">
      <div class="head clickable" @click="toggle(c.id)">
        <NIcon :component="open === c.id ? ChevronDown : ChevronForward" />
        <div class="grow main">
          <code class="line">{{ c.line }}</code>
          <span class="muted small">{{ when(c) }}</span>
        </div>
        <NTag size="small" :bordered="false" :type="tagType(c)"
          :title="c.unresolved ? 'It exited fine, but the warning it was run for is still there: check its output' : undefined">
          <span class="row" style="gap: 6px"><NSpin v-if="c.status === 'running'" :size="12" />{{ tagText(c) }}</span>
        </NTag>
        <NButton v-if="c.status === 'running'" size="tiny" quaternary title="Stop it and everything it started" @click.stop="stop(c.id)">
          <template #icon><NIcon :component="Stop" /></template>
        </NButton>
        <NButton size="tiny" quaternary title="Run again" @click.stop="run(c.line)">
          <template #icon><NIcon :component="Play" /></template>
        </NButton>
      </div>
      <div v-if="open === c.id && c.unresolved" class="unresolved small">
        It exited without errors, but the warning it was run for is still there. Some tools report success even when
        they failed: check the output.
      </div>
      <pre v-if="open === c.id" :ref="(el) => { outBox = el as HTMLElement | null }" class="output"><span v-if="!outputs[c.id]" class="muted">{{ c.status === 'running' ? 'Waiting for output…' : 'No output.' }}</span><span v-html="outputs[c.id]" /></pre>
    </div>
  </div>
</template>

<style scoped>
.cmd { border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
.head { display: flex; align-items: center; gap: 10px; padding: 8px 10px; }
.main { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.line { overflow-wrap: anywhere; }
.unresolved { padding: 6px 12px; background: rgba(240, 160, 32, 0.12); color: #f0a020; }
.output {
  margin: 0; max-height: calc(100vh - 380px); min-height: 120px; overflow: auto; padding: 8px 12px;
  background: #0f0f12; color: #d8d8e0; font: 12.5px/1.5 ui-monospace, 'Cascadia Code', 'JetBrains Mono', monospace;
  white-space: pre-wrap; word-break: break-all;
}
</style>
