<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NButton, NEmpty, NIcon, NSwitch, useMessage } from 'naive-ui'
import { Copy, Trash } from '@vicons/ionicons5'
import { api, type AppView, type Dump } from '../api'

// dump() and dd() captured from the app: Symfony's own HTML rendering, each
// one in a sandboxed frame (it carries the dumper's scripts).
const props = defineProps<{ app: AppView }>()
const emit = defineEmits<{ changed: [] }>()

const message = useMessage()
const dumps = ref<Dump[]>([]) // newest first
const header = ref('')
const heights = ref<Record<number, number>>({})
const busy = ref(false)
let last = 0
let timer = 0

async function poll() {
  if (!props.app.dumps) return
  try {
    const res = await api.dumps(props.app.id, last)
    header.value = res.header
    if (res.last < last) dumps.value = [] // the daemon restarted: ids start over
    if (res.dumps.length) dumps.value = [...res.dumps.reverse(), ...dumps.value].slice(0, 200)
    last = res.last
  } catch {
    // the daemon is restarting; try again on the next tick
  }
}

function reset() {
  dumps.value = []
  heights.value = {}
  last = 0
  poll()
}

async function toggle(on: boolean) {
  busy.value = true
  try {
    const res = await api.update(props.app.id, { dumps: on })
    if (res.error) message.warning(res.error)
    emit('changed')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    busy.value = false
  }
}

async function clear() {
  await api.clearDumps(props.app.id)
  dumps.value = []
}

async function copy(d: Dump) {
  await navigator.clipboard.writeText(d.text)
  message.success('Copied')
}

function srcdoc(d: Dump) {
  return `<!doctype html><html><head><meta charset="utf-8"><base target="_blank">
<style>html,body{margin:0;background:#18171b}pre.sf-dump{margin:0!important;border-radius:0!important}</style>${header.value}</head>
<body>${d.html}<script>
const send = () => parent.postMessage({ laranestDump: ${d.id}, height: document.documentElement.scrollHeight }, '*')
new ResizeObserver(send).observe(document.body)
addEventListener('click', () => setTimeout(send))
send()
<\/script></body></html>`
}

// Each frame reports its height (they can't be read across the sandbox).
function onMessage(e: MessageEvent) {
  const id = e.data?.laranestDump
  if (typeof id !== 'number') return
  const frame = [...document.querySelectorAll<HTMLIFrameElement>('iframe[data-dump]')].find((f) => f.contentWindow === e.source)
  if (frame && Number(frame.dataset.dump) === id) heights.value = { ...heights.value, [id]: Math.min(e.data.height, 2000) }
}

const where = (d: Dump) => (d.name ? `${d.name}:${d.line}` : d.file ? `${d.file}:${d.line}` : '')
const when = (d: Dump) => new Date(d.time).toLocaleTimeString()

watch(() => props.app.id, reset)
watch(() => props.app.dumps, (on) => on && reset())
onMounted(() => {
  addEventListener('message', onMessage)
  poll()
  timer = window.setInterval(poll, 1500)
})
onBeforeUnmount(() => {
  removeEventListener('message', onMessage)
  clearInterval(timer)
})
</script>

<template>
  <div class="stack" style="gap: 12px">
    <div class="row toggle">
      <NSwitch :value="!!app.dumps" :loading="busy" @update:value="toggle" />
      <span class="clickable" @click="!busy && toggle(!app.dumps)">Capture <code>dump()</code> and <code>dd()</code></span>
      <span class="grow" />
      <NButton v-if="dumps.length" size="small" quaternary @click="clear">
        <template #icon><NIcon :component="Trash" /></template>Clear
      </NButton>
    </div>
    <p class="muted small note">
      <template v-if="app.dumps">
        Dumps from requests, queue jobs and artisan commands show up here instead of in the page or the terminal.
      </template>
      <template v-else>
        Turn it on to see every <code>dump()</code> and <code>dd()</code> here, with the file and line that made it and the
        request or command it came from, instead of in the page. The app restarts.
      </template>
    </p>

    <template v-if="app.dumps">
      <NEmpty v-if="!dumps.length"
        :description="app.state === 'stopped' ? 'Start the app to capture its dumps.' : 'Waiting for dumps…'" />
      <div v-for="d in dumps" :key="d.id" class="dump">
        <div class="meta">
          <span class="mono where" :title="d.file">{{ where(d) }}</span>
          <span class="origin mono" :title="d.origin">{{ d.origin }}</span>
          <span class="grow" />
          <span class="muted small">{{ when(d) }}</span>
          <NButton size="tiny" quaternary circle title="Copy as text" @click="copy(d)">
            <template #icon><NIcon :component="Copy" /></template>
          </NButton>
        </div>
        <iframe :data-dump="d.id" :srcdoc="srcdoc(d)" sandbox="allow-scripts" :style="{ height: (heights[d.id] ?? 40) + 'px' }"
          title="Dump" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.toggle { gap: 10px; }
.note { margin: -6px 0 0; }
.dump { border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
.meta { display: flex; gap: 10px; align-items: center; padding: 6px 10px; font-size: 0.82rem; border-bottom: 1px solid var(--border); }
.where { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 50%; }
.origin { color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
iframe { display: block; width: 100%; border: 0; background: #18171b; }
@media (max-width: 560px) {
  .meta { flex-wrap: wrap; }
  .where { max-width: 100%; }
}
</style>
