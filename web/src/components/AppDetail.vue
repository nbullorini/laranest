<script setup lang="ts">
import { computed, h, nextTick, ref, watch } from 'vue'
import {
  NBadge, NButton, NDrawer, NDrawerContent, NIcon, NInput, NRadioButton, NRadioGroup, NSelect, NSwitch, NTabPane, NTabs,
  useDialog, useMessage,
} from 'naive-ui'
import { Play, Reader, Refresh, Stop, Sync, Trash } from '@vicons/ionicons5'
import StateTag from './StateTag.vue'
import WarningList from './WarningList.vue'
import LogViewer from './LogViewer.vue'
import CommandsPanel from './CommandsPanel.vue'
import ProcessToggles from './ProcessToggles.vue'
import GroupSelect from './GroupSelect.vue'
import {
  api, phpOptions, processLabels, type AppResult, type AppView, type PHPInfo, type Processes, type System, type Warning,
} from '../api'

const props = defineProps<{ app: AppView | null; system: System | null; phpInfo: PHPInfo | null; groups: string[] }>()
const emit = defineEmits<{ changed: [id?: string]; close: [] }>()
const show = computed({ get: () => !!props.app, set: (v) => !v && emit('close') })

const dialog = useDialog()
const message = useMessage()
const tab = ref('overview')
const logProcess = ref('web')
const commandsPanel = ref<InstanceType<typeof CommandsPanel> | null>(null)
const busy = ref(false)

// Editable settings, reset whenever another app is opened.
const editId = ref('')
const editPhp = ref('')
const editGroup = ref('')
const editHttps = ref(true)
const editProcs = ref<Processes | null>(null)
watch(() => props.app?.id, () => {
  if (!props.app) return
  editId.value = props.app.id
  editPhp.value = props.app.php
  editGroup.value = props.app.group
  editHttps.value = props.app.https
  editProcs.value = { ...props.app.processes }
  if (!props.app.procs.some((p) => p.name === logProcess.value)) logProcess.value = 'web'
}, { immediate: true })

// Changes that restart the app (the group is cosmetic).
const restartNeeded = computed(() => !!props.app && (
  editId.value !== props.app.id || editPhp.value !== props.app.php ||
  JSON.stringify(editProcs.value) !== JSON.stringify(props.app.processes)))
const dirty = computed(() => restartNeeded.value ||
  (!!props.app && (editGroup.value !== props.app.group || editHttps.value !== props.app.https)))

const phpChoices = computed(() => phpOptions(props.phpInfo, 'Automatic (from composer.json)'))
const downloading = computed(() => props.app?.warnings.find((w) => w.code === 'php_download'))
const problems = computed(() => props.app?.warnings.filter((w) => w.level !== 'info').length ?? 0)

async function run(fn: () => Promise<AppResult | void>, ok?: string) {
  busy.value = true
  try {
    const res = await fn()
    if (res && res.error) message.warning(res.error)
    else if (ok) message.success(ok)
    emit('changed', res ? res.app.id : undefined)
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    busy.value = false
  }
}

const appId = () => props.app!.id
const start = () => run(() => api.start(appId()))
const stop = () => run(() => api.stop(appId()))
const restart = (process = '') => run(() => api.restart(appId(), process))
const redetect = () => run(() => api.redetect(appId()), 'Project re-analyzed')

function fix(w: Warning) {
  const id = appId()
  const diff = Object.entries(w.fix!).map(([k, v]) => {
    const cur = w.current?.[k]
    return `${cur !== undefined ? `- ${k}=${cur}\n` : ''}+ ${k}=${v}`
  }).join('\n')
  dialog.warning({
    title: 'Edit .env',
    content: () => h('pre', { class: 'diff' }, diff),
    positiveText: 'Apply',
    negativeText: 'Cancel',
    onPositiveClick: () => run(() => api.setEnv(id, w.fix!), '.env updated'),
  })
}

// Same flow as the .env fix: show exactly what will run, then run it in the Commands tab.
function runCommand(w: Warning) {
  const app = props.app!
  dialog.info({
    title: 'Run a command',
    content: () => h('div', { class: 'stack', style: 'gap: 8px' }, [
      h('pre', { class: 'diff' }, `laranest ${w.command}`),
      h('span', { class: 'muted small' }, `In ${app.path}, with the project's PHP and Node.`),
    ]),
    positiveText: 'Run',
    negativeText: 'Cancel',
    onPositiveClick: async () => {
      tab.value = 'commands'
      await nextTick()
      await commandsPanel.value?.run(w.command!, w.code)
    },
  })
}

function save() {
  const id = appId()
  run(() => api.update(id, restartNeeded.value
    ? { id: editId.value !== id ? editId.value : undefined, group: editGroup.value, https: editHttps.value, php: editPhp.value, processes: editProcs.value ?? undefined }
    : { group: editGroup.value, https: editHttps.value }), 'Changes saved')
}

function remove() {
  const id = appId()
  dialog.error({
    title: `Remove ${id}`,
    content: 'Its processes are stopped and it is removed from the panel. The project itself is not touched.',
    positiveText: 'Remove',
    negativeText: 'Cancel',
    onPositiveClick: () => run(async () => { await api.remove(id); emit('close') }, 'App removed'),
  })
}

function showLogs(process: string) {
  logProcess.value = process
  tab.value = 'logs'
}
</script>

<template>
  <NDrawer v-model:show="show" :width="760" style="max-width: 100vw" :auto-focus="false">
    <NDrawerContent v-if="app" closable>
      <template #header>
        <div class="row">
          <span class="title">{{ app.id }}</span>
          <StateTag :state="app.state" />
        </div>
        <a :href="app.url" target="_blank" class="mono small link">{{ app.url }}</a>
      </template>

      <div class="row actions">
        <NButton v-if="app.state === 'stopped'" type="primary" :loading="busy" @click="start()">
          <template #icon><NIcon :component="Play" /></template>
          {{ busy && downloading ? `Downloading PHP ${app.php_version}…` : 'Start' }}
        </NButton>
        <template v-else>
          <NButton :loading="busy" @click="restart()">
            <template #icon><NIcon :component="Refresh" /></template>Restart
          </NButton>
          <NButton :loading="busy" @click="stop()">
            <template #icon><NIcon :component="Stop" /></template>Stop
          </NButton>
        </template>
        <NButton quaternary :loading="busy" title="Re-analyze composer.json, .env and the schedule" @click="redetect()">
          <template #icon><NIcon :component="Sync" /></template>Re-detect
        </NButton>
      </div>

      <NTabs v-model:value="tab" type="line" animated>
        <NTabPane name="overview">
          <template #tab>
            <NBadge :value="problems" :show="problems > 0" type="warning" :offset="[10, -2]">Overview</NBadge>
          </template>
          <div class="stack">
            <WarningList :warnings="app.warnings" @fix="fix" @command="runCommand" />
            <dl class="facts">
              <dt>Path</dt><dd class="mono">{{ app.path }}</dd>
              <dt>Laravel</dt><dd>{{ app.detection?.laravel_version || '—' }}</dd>
              <dt>PHP</dt><dd>
                {{ app.php_version || '—' }}
                <span class="muted">· {{ { system: 'system', laranest: 'Laranest', '': 'downloaded on start' }[app.php_source] }}
                  · requires {{ app.detection?.php_constraint || 'any' }}<template v-if="app.detection?.php_max">,
                    ≤ {{ app.detection.php_max }} (supported by Laravel {{ app.detection.laravel_version.split('.')[0].replace(/^[^0-9]*/, '') }})</template></span>
              </dd>
              <template v-if="app.reverb_url"><dt>Reverb</dt><dd class="mono">{{ app.reverb_url }}</dd></template>
              <dt>Packages</dt><dd>{{ Object.keys(app.detection?.packages ?? {}).join(', ') || '—' }}</dd>
            </dl>
          </div>
        </NTabPane>

        <NTabPane name="procs" tab="Processes">
          <table class="procs">
            <thead><tr><th>Process</th><th>Status</th><th class="pid">PID</th><th>Restarts</th><th /></tr></thead>
            <tbody>
              <tr v-for="p in app.procs" :key="p.name">
                <td>{{ processLabels[p.name] ?? p.name }}</td>
                <td><StateTag :state="p.state" /></td>
                <td class="mono pid">{{ p.pid || '—' }}</td>
                <td>{{ p.restarts }}</td>
                <td class="right">
                  <NButton quaternary circle size="small" title="View logs" @click="showLogs(p.name)">
                    <template #icon><NIcon :component="Reader" /></template>
                  </NButton>
                  <NButton quaternary circle size="small" title="Restart" :disabled="app.state === 'stopped'" @click="restart(p.name)">
                    <template #icon><NIcon :component="Refresh" /></template>
                  </NButton>
                </td>
              </tr>
            </tbody>
          </table>
          <p v-if="system?.os === 'windows'" class="muted small">Laranest supervises each process; if it crashes, it restarts on its own.</p>
          <p v-else class="muted small">Each process runs as a systemd unit <code>laranest-{{ app.id }}-*.service</code>; if it crashes, it restarts on its own.</p>
        </NTabPane>

        <NTabPane name="logs" tab="Logs" display-directive="if">
          <div class="stack" style="gap: 8px">
            <NRadioGroup v-model:value="logProcess" size="small">
              <NRadioButton v-for="p in app.procs" :key="p.name" :value="p.name">{{ processLabels[p.name] ?? p.name }}</NRadioButton>
            </NRadioGroup>
            <LogViewer :app-id="app.id" :process="logProcess" />
          </div>
        </NTabPane>

        <NTabPane name="commands" tab="Commands" display-directive="if">
          <CommandsPanel ref="commandsPanel" :app-id="app.id" @finished="emit('changed', app.id)" />
        </NTabPane>

        <NTabPane name="settings" tab="Settings">
          <div class="stack">
            <div class="grid2">
              <label class="field">
                <span>Name (subdomain)</span>
                <NInput v-model:value="editId" :input-props="{ class: 'mono' }" />
              </label>
              <label class="field">
                <span>PHP version</span>
                <NSelect v-model:value="editPhp" :options="phpChoices" />
              </label>
              <label class="field">
                <span>Group</span>
                <GroupSelect v-model="editGroup" :groups="groups" />
              </label>
              <div class="field">
                <span>HTTPS</span>
                <div class="row">
                  <NSwitch v-model:value="editHttps" />
                  <span class="clickable small muted" @click="editHttps = !editHttps">
                    {{ editHttps ? 'With the local certificate (http redirects to https)' : 'HTTP only' }}
                  </span>
                </div>
              </div>
            </div>
            <div>
              <div class="section-title">Processes</div>
              <ProcessToggles v-if="editProcs" v-model="editProcs" :detection="app.detection" />
            </div>
            <div class="row">
              <NButton type="primary" :disabled="!dirty" :loading="busy" @click="save">Save</NButton>
              <span v-if="restartNeeded && app.state !== 'stopped'" class="muted small">The app restarts when saved.</span>
              <span class="grow" />
              <NButton type="error" quaternary @click="remove">
                <template #icon><NIcon :component="Trash" /></template>Remove from panel
              </NButton>
            </div>
          </div>
        </NTabPane>
      </NTabs>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.title { font-size: 1.2rem; font-weight: 600; }
.link { color: var(--brand); text-decoration: none; }
.actions { margin-bottom: 8px; flex-wrap: wrap; }
.facts { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; }
.facts dt { color: var(--muted); }
.facts dd { margin: 0; word-break: break-all; }
.procs { width: 100%; border-collapse: collapse; }
.procs th { text-align: left; font-weight: 500; color: var(--muted); font-size: 0.85rem; }
.procs th, .procs td { padding: 8px 6px; border-bottom: 1px solid var(--border); white-space: nowrap; }
.procs td:first-child { white-space: normal; }
@media (max-width: 480px) {
  .procs .pid { display: none; } /* the least useful column on a phone */
  .facts { grid-template-columns: 1fr; gap: 2px; }
  .facts dd { margin-bottom: 8px; }
}
.right { text-align: right; white-space: nowrap; }
</style>

<style>
.diff { margin: 0; font: 13px/1.6 ui-monospace, monospace; white-space: pre-wrap; }
</style>
