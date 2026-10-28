<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NIcon, NInput, NSwitch, NTag } from 'naive-ui'
import { Add, ArrowUndo, Create, Trash } from '@vicons/ionicons5'
import { processLabels, type CustomProcess, type Processes } from '../api'

// Advanced: change the command of a built-in process, add processes of your
// own, or serve the web through a command listening on a port (Octane…).
const props = defineProps<{ processes: Processes; defaults: Record<string, string> }>()
const commands = defineModel<Record<string, string>>('commands', { required: true })
const custom = defineModel<CustomProcess[]>('custom', { required: true })
const webMode = defineModel<string>('webMode', { required: true })

// The built-in processes that will run (their command can be replaced). The
// web is always there: customizing it serves the app through a command
// listening on a port instead of PHP-FPM / php-cgi.
const builtins = computed(() => {
  const p = props.processes
  return [
    'web',
    p.horizon ? 'horizon' : p.queue ? 'queue' : '',
    p.scheduler ? 'scheduler' : '',
    p.reverb ? 'reverb' : '',
    p.vite ? 'vite' : '',
  ].filter(Boolean)
})

function setCommand(name: string, line: string) {
  commands.value = { ...commands.value, [name]: line }
}

const isCustom = (name: string) => (name === 'web' ? webMode.value === 'port' : commands.value[name] !== undefined)

// Customizing starts from the automatic command, ready to edit (for the web,
// an Octane server on the port Laranest picks).
function customize(name: string) {
  if (name === 'web') {
    webMode.value = 'port'
    if (!commands.value.web) setCommand('web', 'php artisan octane:start --port={port}')
    return
  }
  setCommand(name, props.defaults[name] ?? '')
}

function useAutomatic(name: string) {
  if (name === 'web') webMode.value = ''
  const next = { ...commands.value }
  delete next[name]
  commands.value = next
}

function addCustom() {
  custom.value = [...custom.value, { name: '', command: '' }]
}
function updateCustom(i: number, patch: Partial<CustomProcess>) {
  custom.value = custom.value.map((c, j) => (j === i ? { ...c, ...patch } : c))
}
function removeCustom(i: number) {
  custom.value = custom.value.filter((_, j) => j !== i)
}
</script>

<template>
  <div class="stack advanced" style="gap: 14px">
    <div class="field">
      <span>Commands</span>
      <template v-for="name in builtins" :key="name">
      <div class="cmd-row">
        <span class="label small">{{ name === 'web' ? 'Web' : processLabels[name] ?? name }}</span>
        <template v-if="isCustom(name)">
          <NInput :value="commands[name]" class="mono grow" size="small" :input-props="{ spellcheck: false }"
            @update:value="(v) => setCommand(name, v)" />
          <NTag size="small" type="warning" :bordered="false">Custom</NTag>
          <NButton size="small" quaternary title="Go back to the automatic command" @click="useAutomatic(name)">
            <template #icon><NIcon :component="ArrowUndo" /></template>Use automatic
          </NButton>
        </template>
        <template v-else>
          <code class="grow auto small">{{ defaults[name] }}</code>
          <NTag size="small" :bordered="false">Automatic</NTag>
          <NButton size="small" quaternary title="Edit this command" @click="customize(name)">
            <template #icon><NIcon :component="Create" /></template>Customize
          </NButton>
        </template>
      </div>
      <small v-if="name === 'web' && isCustom('web')" class="muted web-note">
        Instead of PHP-FPM, this command serves the app (Octane, a custom server). It must listen on <code>{port}</code>:
        Laranest picks the port and sends <code>https://…localhost</code> there. Only how web requests are served changes:
        the queue, scheduler and the other processes keep running.
      </small>
      </template>
    </div>

    <div class="field">
      <span>Custom processes</span>
      <div v-for="(c, i) in custom" :key="i" class="custom-row">
        <NSwitch size="small" :value="!c.disabled" title="Enabled" @update:value="(on) => updateCustom(i, { disabled: !on })" />
        <NInput :value="c.name" placeholder="name" class="mono name" size="small" :input-props="{ spellcheck: false }"
          @update:value="(v) => updateCustom(i, { name: v.toLowerCase() })" />
        <NInput :value="c.command" placeholder="php artisan queue:work --queue=emails" class="mono grow" size="small"
          :input-props="{ spellcheck: false }" @update:value="(v) => updateCustom(i, { command: v })" />
        <NButton size="small" quaternary type="error" title="Remove" @click="removeCustom(i)">
          <template #icon><NIcon :component="Trash" /></template>
        </NButton>
      </div>
      <div>
        <NButton size="small" @click="addCustom">
          <template #icon><NIcon :component="Add" /></template>Add process
        </NButton>
      </div>
    </div>

    <small class="muted">
      Commands run in the project's folder; <code>php</code>, <code>composer</code>, <code>npm</code>, <code>npx</code> and
      <code>node</code> are the project's. Other programs must be on your PATH. Placeholders: <code>{url}</code>
      (the app's URL), <code>{path}</code>, <code>{reverb_port}</code>. They restart on their own if they exit, and their
      output shows in Logs.
    </small>
  </div>
</template>

<style scoped>
.cmd-row, .custom-row { display: flex; align-items: center; gap: 8px; }
.cmd-row .label { width: 110px; flex-shrink: 0; }
.cmd-row .auto { opacity: 0.75; overflow-wrap: anywhere; }
.web-note { margin: -2px 0 6px 118px; }
@media (max-width: 560px) { .web-note { margin-left: 0; } }
.custom-row .name { width: 130px; flex-shrink: 0; }
@media (max-width: 560px) {
  .cmd-row, .custom-row { flex-wrap: wrap; }
  .cmd-row .label { width: 100%; }
  .custom-row .name { width: auto; flex: 1; }
  .custom-row .grow { flex-basis: 100%; }
}
</style>
