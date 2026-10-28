<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { NAlert, NBadge, NButton, NIcon, NTooltip, useDialog, useMessage } from 'naive-ui'
import { Add, FileTray, Settings } from '@vicons/ionicons5'
import AppsTable from './components/AppsTable.vue'
import AddAppDialog from './components/AddAppDialog.vue'
import AppDetail from './components/AppDetail.vue'
import SettingsDialog from './components/SettingsDialog.vue'
import PHPVersions from './components/PHPVersions.vue'
import { api, browserTrusts, type AppView, type PHPInfo, type System, type TrustStatus, type UpdateStatus, type WSLStatus } from './api'
import { applyUpdate } from './update'
import { confirmKeepAlive } from './wsl'
import { trustWindows } from './trust'

const message = useMessage()
const dialog = useDialog()
const wsl = ref<WSLStatus | null>(null)
const trust = ref<TrustStatus | null>(null)
const needsTrust = computed(() => apps.value.some((a) => a.https) && !browserTrusts(trust.value, wsl.value))
async function doTrustWindows() {
  const st = await trustWindows(message)
  if (st) trust.value = st
}
const keepAlive = () => wsl.value && confirmKeepAlive(dialog, message, wsl.value, (st) => (wsl.value = st))
const system = ref<System | null>(null)
const update = ref<UpdateStatus | null>(null)
const updating = ref(false)
async function doUpdate() {
  updating.value = true
  await applyUpdate(message)
  updating.value = false
}
const phpInfo = ref<PHPInfo | null>(null)
const apps = ref<AppView[]>([])
const loaded = ref(false)
const offline = ref(false)
const selectedId = ref<string | null>(null)
const showAdd = ref(false)
const showSettings = ref(false)
const showPHP = ref(false)
const phpUpdates = computed(() => phpInfo.value?.installed.filter((v) => v.update).length ?? 0)

function openApp(id: string) {
  showPHP.value = false
  selectedId.value = id
}
const busy = ref<string | null>(null)

const selected = computed(() => apps.value.find((a) => a.id === selectedId.value) ?? null)

async function refresh() {
  try {
    const [s, a] = await Promise.all([api.system(), api.apps()])
    system.value = s
    apps.value = a
    offline.value = false
  } catch {
    offline.value = true
  } finally {
    loaded.value = true
  }
}

async function toggle(app: AppView) {
  busy.value = app.id
  try {
    const res = app.state === 'stopped' ? await api.start(app.id) : await api.stop(app.id)
    if (res.error) message.warning(`${app.id}: ${res.error}`)
    await refresh()
  } catch (e) {
    message.error(`${app.id}: ${(e as Error).message}`)
  } finally {
    busy.value = null
  }
}

async function restart(app: AppView) {
  busy.value = `${app.id}:restart`
  try {
    const res = await api.restart(app.id)
    if (res.error) message.warning(`${app.id}: ${res.error}`)
    else message.success(`${app.id} restarted`)
    await refresh()
  } catch (e) {
    message.error(`${app.id}: ${(e as Error).message}`)
  } finally {
    busy.value = null
  }
}

function onChanged(id?: string) {
  if (id) selectedId.value = id // follows a rename
  refresh()
}

function onAdded(id: string) {
  selectedId.value = id
  refresh()
}

// Groups already in use, for the selectors.
const groups = computed(() => [...new Set(apps.value.map((a) => a.group).filter(Boolean))].sort())

async function toggleGroup(list: AppView[], start: boolean) {
  for (const app of list) {
    if ((app.state === 'stopped') !== start) continue
    busy.value = app.id
    try {
      const res = start ? await api.start(app.id) : await api.stop(app.id)
      if (res.error) message.warning(`${app.id}: ${res.error}`)
    } catch (e) {
      message.error(`${app.id}: ${(e as Error).message}`)
    }
  }
  busy.value = null
  refresh()
}

// Not polled: the catalog rarely changes and may hit the network.
async function refreshPHP() {
  phpInfo.value = await api.php().catch(() => phpInfo.value)
}

let timer: number | undefined
onMounted(() => {
  refresh()
  refreshPHP()
  api.wsl().then((st) => (wsl.value = st)).catch(() => {})
  api.trust().then((st) => (trust.value = st)).catch(() => {})
  api.checkUpdate().then((st) => (update.value = st)).catch(() => {})
  timer = window.setInterval(refresh, 3000)
})
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <header class="topbar">
    <div class="brand"><img src="/logo.png" alt="" class="logo" /> Laranest</div>
    <span class="grow" />
    <NBadge :value="phpUpdates" :show="phpUpdates > 0" type="warning">
      <NButton quaternary @click="showPHP = true">PHP</NButton>
    </NBadge>
    <NTooltip>
      <template #trigger>
        <NButton quaternary circle @click="showSettings = true">
          <template #icon><NIcon :component="Settings" /></template>
        </NButton>
      </template>
      Settings
    </NTooltip>
    <NButton type="primary" aria-label="Add app" @click="showAdd = true">
      <template #icon><NIcon :component="Add" /></template>
      <span class="add-label">Add app</span>
    </NButton>
  </header>

  <main class="content stack">
    <NAlert v-if="offline" type="error" title="Offline">Cannot reach the Laranest daemon.</NAlert>
    <NAlert v-if="update?.available && !offline" type="info" title="A new version of Laranest is available">
      Your apps, settings and PHP versions are kept{{ system?.os === 'windows' ? '; the apps restart with Laranest' : ' and keep running' }}.
      <div style="margin-top: 8px"><NButton size="small" type="primary" :loading="updating" @click="doUpdate">Update now</NButton></div>
    </NAlert>
    <NAlert v-if="needsTrust" type="warning" title="Your browser does not trust Laranest's certificates yet">
      HTTPS apps will show "Your connection is not private" until you trust Laranest's local CA once.
      <!-- The button needs the Windows profile captured by `laranest install`. -->
      <div v-if="trust?.windows !== undefined" style="margin-top: 8px">
        <NButton size="small" @click="doTrustWindows">Trust on Windows…</NButton>
        <span class="muted small" style="margin-left: 8px">Windows will ask you to confirm (no administrator rights needed).</span>
      </div>
      <div v-else style="margin-top: 8px">Run in a terminal: <code>laranest trust</code> (it asks for your sudo password once).</div>
    </NAlert>
    <NAlert v-if="wsl?.supported && !wsl.keep_alive" type="warning" title="WSL shuts down when no terminal is open">
      A few seconds after the last terminal closes, WSL shuts the distro down and Laranest and your apps stop responding.
      <div style="margin-top: 8px"><NButton size="small" @click="keepAlive">Keep WSL running…</NButton></div>
    </NAlert>
    <NAlert v-else-if="system?.proxy_error" type="error" title="The apps are not reachable">
      <div>{{ system.proxy_error }}</div>
      <div v-if="system.proxy_hint" class="small" style="margin-top: 4px">{{ system.proxy_hint }}</div>
      <NButton size="small" style="margin-top: 8px" @click="showSettings = true">Change port</NButton>
    </NAlert>

    <section v-if="loaded && !apps.length && !offline" class="empty">
      <NIcon :component="FileTray" size="40" class="muted" />
      <h2>No apps yet</h2>
      <p class="muted">Add a Laravel project by its path; Laranest figures out what it needs.</p>
      <NButton type="primary" @click="showAdd = true">
        <template #icon><NIcon :component="Add" /></template>
        Add app
      </NButton>
    </section>

    <div v-if="apps.length" class="table-wrap">
      <AppsTable :apps="apps" :busy="busy" @open="(id) => (selectedId = id)" @toggle="toggle" @restart="restart" @group="toggleGroup" />
    </div>
  </main>

  <AddAppDialog v-model:show="showAdd" :system="system" :php-info="phpInfo" :groups="groups" @added="onAdded" @settings="showSettings = true" />
  <SettingsDialog v-model:show="showSettings" :system="system" :wsl="wsl" :trust="trust" :update="update" @changed="refresh"
    @update="(st) => (update = st)"
    @trust="(st) => (trust = st)"
    @php="showSettings = false; showPHP = true" @wsl="(st) => (wsl = st)" />
  <PHPVersions v-model:show="showPHP" :info="phpInfo" @changed="(info) => { phpInfo = info; refresh() }" @open-app="openApp" />
  <AppDetail :app="selected" :system="system" :php-info="phpInfo" :groups="groups" @changed="(id) => { onChanged(id); refreshPHP() }" @close="selectedId = null" />
</template>

<style scoped>
.topbar {
  display: flex; align-items: center; gap: 8px; padding: 10px 24px;
  background: var(--surface); border-bottom: 1px solid var(--border);
  position: sticky; top: 0; z-index: 10;
}
.brand { font-weight: 700; font-size: 1.1rem; color: var(--brand); display: flex; gap: 8px; align-items: center; }
.logo { width: 32px; height: 32px; }
@media (max-width: 480px) {
  .topbar { padding: 10px 16px; }
}
@media (max-width: 360px) {
  .add-label { display: none; } /* icon only on the narrowest phones */
}
.empty { text-align: center; padding: 48px 24px; border: 1px dashed var(--border); border-radius: 12px; }
.empty h2 { margin: 8px 0; font-size: 1.15rem; }
.table-wrap {
  background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 4px 8px;
}
</style>
