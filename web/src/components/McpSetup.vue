<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NIcon, NModal, useMessage } from 'naive-ui'
import { Copy } from '@vicons/ionicons5'
import type { System, WSLStatus } from '../api'

// How to connect AI assistants to Laranest's MCP server (`laranest mcp`).
// The full path to the binary, because desktop apps often don't have
// ~/.local/bin (or a new PATH entry) in their environment.
const props = defineProps<{ system: System | null; wsl: WSLStatus | null }>()
const show = defineModel<boolean>('show', { required: true })
const message = useMessage()

const exe = computed(() => props.system?.executable || 'laranest')
const claudeCode = computed(() => `claude mcp add -s user laranest -- ${quote(exe.value)} mcp`)
const config = computed(() => JSON.stringify({ mcpServers: { laranest: { command: exe.value, args: ['mcp'] } } }, null, 2))
// From AI apps running on Windows, reach the Laranest inside WSL.
const windowsConfig = computed(() => JSON.stringify({
  mcpServers: { laranest: { command: 'wsl.exe', args: ['-d', props.wsl?.distro || 'Ubuntu', '--', exe.value, 'mcp'] } },
}, null, 2))

function quote(s: string) {
  return /\s/.test(s) ? `"${s}"` : s
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    message.success('Copied')
  } catch {
    message.error('Could not copy: select the text instead')
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" title="AI assistants (MCP)" style="width: 640px; max-width: calc(100vw - 32px)">
  <div class="stack" style="gap: 12px">
    <div class="muted">
      Let Claude, Cursor, VS Code or any MCP client manage your apps: add and configure them, start and stop them, read
      their logs, fix their warnings and run commands. It talks to this Laranest, with the same rules as the panel, and
      your client asks you before actions that change things.
    </div>
    <div class="snippet">
      <div class="row"><span class="grow small">Claude Code (<code>-s user</code>: in every folder, not only the current one)</span>
        <NButton size="tiny" quaternary @click="copy(claudeCode)"><template #icon><NIcon :component="Copy" /></template>Copy</NButton></div>
      <pre>{{ claudeCode }}</pre>
    </div>
    <div class="snippet">
      <div class="row"><span class="grow small">Claude Desktop, Cursor, VS Code… (their MCP config)</span>
        <NButton size="tiny" quaternary @click="copy(config)"><template #icon><NIcon :component="Copy" /></template>Copy</NButton></div>
      <pre>{{ config }}</pre>
    </div>
    <div v-if="wsl?.is_wsl" class="snippet">
      <div class="row"><span class="grow small">AI apps running on Windows (reaching this WSL)</span>
        <NButton size="tiny" quaternary @click="copy(windowsConfig)"><template #icon><NIcon :component="Copy" /></template>Copy</NButton></div>
      <pre>{{ windowsConfig }}</pre>
    </div>
    <small class="muted">Then ask your assistant, e.g. “Add ~/code/shop to Laranest and get it running” or “Why does the queue keep failing?”.</small>
  </div>
  </NModal>
</template>

<style scoped>
.snippet { display: flex; flex-direction: column; gap: 2px; }
pre {
  margin: 0; padding: 8px 10px; border-radius: 6px; background: var(--chip); overflow-x: auto;
  font: 12px/1.5 ui-monospace, 'Cascadia Code', 'JetBrains Mono', monospace;
}
</style>
