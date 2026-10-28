<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NEmpty, NIcon, NInput, NSelect, NSpin, NTag, useDialog, useMessage } from 'naive-ui'
import { Refresh, Search, Trash } from '@vicons/ionicons5'
import { api, type LaravelLog, type LaravelLogEntry } from '../api'

// The app's own log (storage/logs): what Laravel records about requests,
// jobs and commands, by level, with the stack trace of each exception.
const props = defineProps<{ appId: string }>()

const dialog = useDialog()
const message = useMessage()
const log = ref<LaravelLog | null>(null)
const file = ref('')
const level = ref('')
const search = ref('')
const loading = ref(false)
const open = ref(new Set<number>())

const levelOptions = [
  { label: 'All levels', value: '' },
  { label: 'Warnings and errors', value: 'warning' },
  { label: 'Errors', value: 'error' },
]
const fileOptions = computed(() => (log.value?.files ?? []).map((f) => ({ label: `${f.name} · ${size(f.size)}`, value: f.name })))
const tagType = (l: LaravelLogEntry['level']) =>
  ({ debug: 'default', info: 'info', notice: 'info', warning: 'warning' } as const)[l as 'debug'] ?? 'error'

function size(n: number) {
  return n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(0)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`
}

async function load(quiet = false) {
  if (!quiet) loading.value = true
  try {
    const res = await api.laravelLog(props.appId, { file: file.value, level: level.value, q: search.value.trim() })
    if (!quiet || JSON.stringify(res.entries[0]) !== JSON.stringify(log.value?.entries[0])) open.value = new Set()
    log.value = res
    file.value = res.file
  } catch (e) {
    if (!quiet) message.error((e as Error).message)
  } finally {
    loading.value = false
  }
}

function toggle(i: number) {
  const next = new Set(open.value)
  if (!next.delete(i)) next.add(i)
  open.value = next
}

function clear() {
  const name = file.value
  dialog.warning({
    title: `Clear ${name}`,
    content: 'Its contents are deleted. Laravel keeps writing to it.',
    positiveText: 'Clear',
    negativeText: 'Cancel',
    onPositiveClick: async () => {
      try {
        await api.clearLaravelLog(props.appId, name)
        await load()
      } catch (e) {
        message.error((e as Error).message)
      }
    },
  })
}

let searchTimer = 0
watch(search, () => {
  clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => load(), 300)
})
watch([file, level], () => load())
watch(() => props.appId, () => {
  file.value = ''
  load()
}, { immediate: true })

// New entries show up while the tab is open.
const timer = window.setInterval(() => !loading.value && load(true), 4000)
onBeforeUnmount(() => {
  clearInterval(timer)
  clearTimeout(searchTimer)
})
</script>

<template>
  <div class="stack" style="gap: 10px">
    <div class="toolbar">
      <NSelect v-if="fileOptions.length > 1" v-model:value="file" :options="fileOptions" size="small" class="file" />
      <NSelect v-model:value="level" :options="levelOptions" size="small" class="level" />
      <NInput v-model:value="search" size="small" placeholder="Search" clearable class="grow">
        <template #prefix><NIcon :component="Search" /></template>
      </NInput>
      <NButton size="small" quaternary circle title="Refresh" @click="load()">
        <template #icon><NIcon :component="Refresh" /></template>
      </NButton>
      <NButton v-if="log?.file" size="small" quaternary circle type="error" title="Clear this file" @click="clear">
        <template #icon><NIcon :component="Trash" /></template>
      </NButton>
    </div>

    <NSpin :show="loading && !log">
      <NEmpty v-if="log && !log.file" description="No log files in storage/logs yet." />
      <NEmpty v-else-if="log && !log.entries.length"
        :description="search || level ? 'No entries match.' : 'The log is empty.'" />
      <div v-else class="entries">
        <div v-for="(e, i) in log?.entries" :key="i" class="entry" :class="{ open: open.has(i) }">
          <div class="head" :class="{ clickable: e.details }" @click="e.details && toggle(i)">
            <NTag size="small" :type="tagType(e.level)" :bordered="false" class="lvl">{{ e.level }}</NTag>
            <span class="msg">{{ e.message || '(no message)' }}</span>
            <span class="time mono">{{ e.time.replace('T', ' ').slice(0, 19) }}</span>
          </div>
          <pre v-if="e.details && open.has(i)" class="details">{{ e.details }}</pre>
        </div>
        <p v-if="log?.truncated" class="muted small">Showing the newest entries only.</p>
      </div>
    </NSpin>
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.toolbar .file { width: 240px; }
.toolbar .level { width: 180px; }
.toolbar .grow { flex: 1; min-width: 140px; }
.entries { display: flex; flex-direction: column; gap: 2px; max-height: calc(100vh - 300px); min-height: 200px; overflow: auto; }
.entry { border-bottom: 1px solid var(--border); }
.head { display: flex; gap: 10px; align-items: baseline; padding: 7px 4px; }
.head.clickable { cursor: pointer; }
.head.clickable:hover { background: color-mix(in srgb, var(--muted) 8%, transparent); }
.lvl { flex-shrink: 0; text-transform: uppercase; font-size: 0.7rem; }
.msg { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.entry:not(.open) .msg { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.time { color: var(--muted); font-size: 0.78rem; flex-shrink: 0; }
.details {
  margin: 0 0 8px; padding: 8px 12px; border-radius: 6px; max-height: 360px; overflow: auto;
  background: #0f0f12; color: #d8d8e0; font: 12px/1.5 ui-monospace, monospace; white-space: pre-wrap; word-break: break-all;
}
@media (max-width: 560px) {
  .toolbar .file, .toolbar .level { width: 100%; }
  .head { flex-wrap: wrap; }
  .time { width: 100%; order: 3; }
}
</style>
