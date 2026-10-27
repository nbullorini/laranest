<script setup lang="ts">
import { computed } from 'vue'
import { NSelect } from 'naive-ui'

const model = defineModel<string>({ required: true })
const props = defineProps<{ groups: string[] }>()

// `tag` lets the user type a new group name; clearing means "no group".
const options = computed(() => props.groups.map((g) => ({ label: g, value: g })))
const value = computed({
  get: () => model.value || null,
  set: (v: string | null) => (model.value = (v ?? '').trim()),
})
</script>

<template>
  <NSelect v-model:value="value" :options="options" filterable tag clearable placeholder="No group (type to create one)" />
</template>
