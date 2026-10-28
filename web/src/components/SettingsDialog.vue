<script setup lang="ts">
import { ref, watch } from 'vue'
import { NAlert, NButton, NDynamicInput, NInputNumber, NModal, NSwitch, useDialog, useMessage } from 'naive-ui'
import { api, type System, type TrustStatus, type UpdateStatus, type WSLStatus } from '../api'
import { applyUpdate } from '../update'
import { confirmKeepAlive } from '../wsl'
import { trustWindows } from '../trust'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ system: System | null; wsl: WSLStatus | null; trust: TrustStatus | null; update: UpdateStatus | null }>()
const emit = defineEmits<{ changed: []; php: []; wsl: [st: WSLStatus]; trust: [st: TrustStatus]; update: [st: UpdateStatus] }>()

const checking = ref(false)
const updating = ref(false)
async function checkUpdate() {
  checking.value = true
  try {
    const st = await api.checkUpdate(true)
    emit('update', st)
    if (st.error) message.error(st.error)
    else if (!st.available) message.success('Laranest is up to date')
  } finally {
    checking.value = false
  }
}
async function doUpdate() {
  updating.value = true
  await applyUpdate(message)
  updating.value = false
}
const message = useMessage()
const dialog = useDialog()

function setKeepAlive(on: boolean) {
  if (!props.wsl) return
  if (on) return confirmKeepAlive(dialog, message, props.wsl, (st) => emit('wsl', st))
  applyWSL({ keep_alive: false }, 'Reverted: WSL will shut down again without terminals (after WSL restarts)')
}

function setAutostart(on: boolean) {
  applyWSL({ autostart: on }, on ? 'Laranest will start when you sign in to Windows' : 'Autostart disabled')
}

async function applyWSL(patch: { keep_alive?: boolean; autostart?: boolean }, ok: string) {
  try {
    emit('wsl', await api.setWSL(patch))
    message.success(ok)
  } catch (e) {
    message.error((e as Error).message)
  }
}

const port = ref<number | null>(80)
const httpsPort = ref<number | null>(443)
async function doTrustWindows() {
  const st = await trustWindows(message)
  if (st) emit('trust', st)
}
const scanDirs = ref<string[]>([])
const saving = ref(false)
watch(show, (v) => {
  if (!v || !props.system) return
  port.value = props.system.http_port
  httpsPort.value = props.system.https_port
  scanDirs.value = [...props.system.scan_dirs]
})

async function save() {
  if (!port.value) return
  saving.value = true
  try {
    await api.updateSystem({ http_port: port.value, https_port: httpsPort.value ?? undefined, scan_dirs: scanDirs.value.filter((d) => d.trim()) })
    message.success('Settings saved')
    emit('changed')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" title="Settings" style="width: 560px; max-width: calc(100vw - 32px)">
    <div v-if="system" class="stack">
      <div class="grid2">
        <label class="field">
          <span>HTTP port</span>
          <NInputNumber v-model:value="port" :min="1" :max="65535" :show-button="false" />
        </label>
        <label class="field">
          <span>HTTPS port</span>
          <NInputNumber v-model:value="httpsPort" :min="1" :max="65535" :show-button="false" />
        </label>
      </div>
      <small class="muted" style="margin-top: -8px">With 80 and 443 URLs stay clean (<code>https://app.{{ system.domain }}</code>).</small>
      <NAlert v-if="system.tls_error" type="error" title="HTTPS could not start">{{ system.tls_error }}</NAlert>
      <NAlert v-if="system.proxy_error" type="error" :title="system.proxy_error">{{ system.proxy_hint }}</NAlert>
      <NAlert v-else type="success">Proxy listening on port {{ system.http_port }}.</NAlert>

      <div class="field">
        <span>Laranest</span>
        <div class="row">
          <span class="grow small">Version <code>{{ system.version }}</code>{{ update?.available ? ' · a new version is available' : '' }}</span>
          <NButton v-if="update?.available" size="small" type="primary" :loading="updating" @click="doUpdate">Update</NButton>
          <NButton v-else size="small" :loading="checking" @click="checkUpdate">Check for updates</NButton>
        </div>
      </div>

      <div class="field">
        <span>Folders to look for projects in</span>
        <NDynamicInput v-model:value="scanDirs" placeholder="/path/to/projects or ~/code" :min="1" />
        <small class="muted">Laravel projects are searched up to 4 levels deep.<template v-if="wsl?.is_wsl"> Avoid <code>/mnt/c</code>: the Windows disk is very slow from WSL.</template></small>
      </div>

      <div v-if="trust?.ready" class="field">
        <span>HTTPS certificates</span>
        <div v-if="trust.windows !== undefined" class="row">
          <span class="grow small">Windows browsers: {{ trust.windows ? '✓ trusted' : '✗ not trusted' }}</span>
          <NButton v-if="!trust.windows" size="small" @click="doTrustWindows">Trust on Windows…</NButton>
        </div>
        <div v-if="system?.os !== 'windows'" class="row">
          <span class="grow small">{{ wsl?.is_wsl ? 'Linux (inside WSL)' : system?.os === 'darwin' ? 'This Mac (Keychain: Safari, Chrome)' : 'This Linux (system, Chrome, Firefox)' }}: {{ trust.system ? '✓ trusted' : '✗ not trusted' }}</span>
          <code v-if="!trust.system" class="small">laranest trust{{ wsl?.is_wsl ? ' --linux' : '' }}</code>
        </div>
        <small class="muted">Local CA: {{ trust.subject }}. Laranest's apps (PHP, Node) already trust it.</small>
      </div>

      <div v-if="wsl?.is_wsl" class="field">
        <span>WSL</span>
        <template v-if="wsl.supported">
          <label class="row">
            <NSwitch :value="wsl.keep_alive" @update:value="setKeepAlive" />
            <span class="clickable" @click="setKeepAlive(!wsl.keep_alive)">Keep WSL running even with no terminal open</span>
          </label>
          <label class="row">
            <NSwitch :value="wsl.autostart" :disabled="!wsl.keep_alive" @update:value="setAutostart" />
            <span class="clickable" @click="wsl.keep_alive && setAutostart(!wsl.autostart)">Start Laranest when signing in to Windows</span>
          </label>
          <small class="muted">Edits <code>{{ wsl.config_file }}</code> (all your distros). Changes take effect the next time WSL starts.</small>
        </template>
        <small v-else class="muted">Not available: run <code>laranest install</code> from a WSL terminal so it can find your Windows profile.</small>
      </div>

      <div class="field">
        <span>PHP</span>
        <div class="row">
          <span class="muted small grow">
            {{ system.runtimes.map((r) => r.version).join(', ') || 'No version installed' }}
          </span>
          <NButton size="small" @click="emit('php')">Manage PHP versions</NButton>
        </div>
        <small v-for="v in system.cli_only ?? []" :key="v" class="muted">
          System PHP {{ v }} has no FPM: <code>sudo apt install php{{ v }}-fpm</code>
        </small>
      </div>
      <div class="muted small">Laranest {{ system.version }} · panel at {{ system.panel_url }}</div>
    </div>
    <template #action>
      <div class="row" style="justify-content: flex-end">
        <NButton quaternary @click="show = false">Close</NButton>
        <NButton type="primary" :loading="saving" @click="save">Save</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
/* Long labels and commands wrap onto a new line on phones. */
.row { flex-wrap: wrap; }
</style>
