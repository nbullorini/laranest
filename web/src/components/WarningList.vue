<script setup lang="ts">
import { NAlert, NButton, NIcon } from 'naive-ui'
import { Construct, Play } from '@vicons/ionicons5'
import type { Warning } from '../api'

defineProps<{ warnings: Warning[] }>()
defineEmits<{ fix: [warning: Warning]; command: [warning: Warning] }>()

const type = { error: 'error', warning: 'warning', info: 'info' } as const
</script>

<template>
  <div class="stack" style="gap: 8px">
    <NAlert v-if="!warnings.length" type="success" :show-icon="true">No problems found.</NAlert>
    <NAlert v-for="w in warnings" :key="w.code" :type="type[w.level]" :title="w.message">
      <div class="warning">
        <code v-if="w.hint" class="hint">{{ w.hint }}</code>
        <span class="grow" />
        <NButton v-if="w.command" size="small" :title="`Run laranest ${w.command} in the project`" @click="$emit('command', w)">
          <template #icon><NIcon :component="Play" /></template>
          Run
        </NButton>
        <NButton v-if="w.fix" size="small" @click="$emit('fix', w)">
          <template #icon><NIcon :component="Construct" /></template>
          Fix .env
        </NButton>
      </div>
    </NAlert>
  </div>
</template>

<style scoped>
.warning { display: flex; gap: 12px; align-items: center; }
.hint { font-size: 0.85em; opacity: 0.85; }
</style>
