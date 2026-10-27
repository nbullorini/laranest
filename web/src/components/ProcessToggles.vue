<script setup lang="ts">
import { computed } from 'vue'
import { NSwitch } from 'naive-ui'
import type { Detection, Processes } from '../api'

const model = defineModel<Processes>({ required: true })
const props = defineProps<{ detection: Detection | null }>()

const pkgs = computed(() => props.detection?.packages ?? {})
const scheduled = computed(() => props.detection?.scheduled_tasks ?? -1)

const items = computed(() => [
  { key: 'queue' as const, label: 'Queue worker', cmd: 'queue:work', help: 'Processes queued jobs.', show: !model.value.horizon },
  { key: 'horizon' as const, label: 'Horizon', cmd: 'horizon', help: 'Replaces the queue worker (needs Redis).', show: !!pkgs.value.horizon },
  {
    key: 'scheduler' as const, label: 'Scheduler', cmd: 'schedule:work', show: true,
    help: scheduled.value >= 0 ? `${scheduled.value} scheduled task(s).` : 'Runs the scheduled tasks.',
  },
  { key: 'reverb' as const, label: 'Reverb', cmd: 'reverb:start', help: 'WebSocket server.', show: !!pkgs.value.reverb },
  { key: 'vite' as const, label: 'Vite dev server', cmd: 'npm run dev', help: 'Hot-reloads the assets.', show: !!props.detection?.uses_vite },
].filter((i) => i.show))
</script>

<template>
  <div class="toggles">
    <div v-for="i in items" :key="i.key" class="toggle">
      <NSwitch v-model:value="model[i.key]" />
      <!-- NSwitch isn't a native input, so a <label> wouldn't toggle it -->
      <div class="clickable" @click="model[i.key] = !model[i.key]">
        <div>{{ i.label }} <code class="cmd">{{ i.cmd }}</code></div>
        <div class="muted small">{{ i.help }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toggles { display: grid; gap: 12px; }
.toggle { display: flex; gap: 12px; align-items: center; cursor: pointer; }
.cmd { font-size: 0.8em; opacity: 0.7; margin-left: 4px; }
</style>
