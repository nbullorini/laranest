<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { AnsiUp } from 'ansi_up'
import { NButton, NIcon } from 'naive-ui'
import { ArrowDown } from '@vicons/ionicons5'
import { api, type LogLine } from '../api'

const props = defineProps<{ appId: string; process: string }>()

const MAX_LINES = 2000
const lines = ref<{ key: number; html: string; stream: string; time: string }[]>([])
const follow = ref(true)
const box = ref<HTMLElement | null>(null)
const ansi = new AnsiUp() // escapes HTML before adding color spans
let source: EventSource | null = null
let seq = 0

function connect() {
  source?.close()
  lines.value = []
  follow.value = true
  source = new EventSource(api.logsURL(props.appId, props.process))
  source.onmessage = (ev) => {
    const l = JSON.parse(ev.data) as LogLine
    lines.value.push({ key: seq++, html: ansi.ansi_to_html(l.text), stream: l.stream, time: new Date(l.time).toLocaleTimeString() })
    if (lines.value.length > MAX_LINES) lines.value.splice(0, lines.value.length - MAX_LINES)
    if (follow.value) nextTick(scrollToEnd)
  }
}

function scrollToEnd() {
  box.value?.scrollTo({ top: box.value.scrollHeight })
}

function onScroll() {
  const el = box.value
  if (el) follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

function jump() {
  follow.value = true
  scrollToEnd()
}

watch(() => [props.appId, props.process], connect, { immediate: true })
onBeforeUnmount(() => source?.close())
</script>

<template>
  <div class="logs-wrap">
    <div ref="box" class="logs" @scroll="onScroll">
      <div v-if="!lines.length" class="empty">No logs yet.</div>
      <div v-for="l in lines" :key="l.key" class="line" :class="l.stream">
        <span class="time">{{ l.time }}</span>
        <span class="text" v-html="l.html" />
      </div>
    </div>
    <NButton v-if="!follow" class="jump" circle type="primary" title="Jump to the end" @click="jump">
      <template #icon><NIcon :component="ArrowDown" /></template>
    </NButton>
  </div>
</template>

<style scoped>
.logs-wrap { position: relative; }
.logs {
  height: calc(100vh - 260px); min-height: 300px; overflow: auto; padding: 8px 12px; border-radius: 8px;
  background: #0f0f12; color: #d8d8e0; font: 12.5px/1.5 ui-monospace, 'Cascadia Code', 'JetBrains Mono', monospace;
}
.line { display: flex; gap: 12px; white-space: pre-wrap; word-break: break-all; }
.line.sys { color: #8a8aa0; font-style: italic; }
.time { color: #5c5c70; flex-shrink: 0; user-select: none; }
.empty { color: #5c5c70; }
.jump { position: absolute; right: 16px; bottom: 16px; }
</style>
