<script setup lang="ts">
import { computed } from 'vue'
import { NTag } from 'naive-ui'

const props = defineProps<{ state: string }>()

type TagType = 'success' | 'warning' | 'info' | 'error' | 'default'
const map: Record<string, { label: string; type: TagType }> = {
  running: { label: 'Running', type: 'success' },
  partial: { label: 'Partial', type: 'warning' },
  starting: { label: 'Starting', type: 'info' },
  backoff: { label: 'Restarting', type: 'warning' },
  stopping: { label: 'Stopping', type: 'default' },
  failed: { label: 'Failed', type: 'error' },
  stopped: { label: 'Stopped', type: 'default' },
}
const t = computed(() => map[props.state] ?? { label: props.state, type: 'default' as TagType })
</script>

<template>
  <NTag :type="t.type" size="small" round :bordered="false">{{ t.label }}</NTag>
</template>
