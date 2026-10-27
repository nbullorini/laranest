<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NIcon, NModal, NTag, useMessage } from 'naive-ui'
import { CloudDownload, Refresh, Trash } from '@vicons/ionicons5'
import { api, type PHPInfo, type PHPVersion } from '../api'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ info: PHPInfo | null }>()
const emit = defineEmits<{ changed: [info: PHPInfo]; openApp: [id: string] }>()
const message = useMessage()

const busy = ref<string | null>(null)
const checking = ref(false)
const checkedAt = ref<Date | null>(null)

const updates = computed(() => (props.info?.installed ?? []).filter((v) => v.update))
// static-php's generic set is the fallback while Laranest's own builds are unreachable.
const reducedSet = computed(() => (props.info?.available ?? []).some((r) => r.source === 'static-php'))

function originLabel(v: PHPVersion) {
  if (v.source === 'system') return 'system (apt)'
  return v.origin === 'static-php' ? 'Laranest · generic set' : 'Laranest'
}

async function check() {
  checking.value = true
  try {
    const info = await api.php(true)
    emit('changed', info)
    checkedAt.value = new Date()
    const n = info.installed.filter((v) => v.update).length
    if (info.error) message.error(info.error)
    else message.info(n ? `${n} update(s) available` : 'Everything is up to date')
  } finally {
    checking.value = false
  }
}

async function run(version: string, fn: () => Promise<PHPInfo>, ok: string) {
  busy.value = version
  try {
    emit('changed', await fn())
    message.success(ok)
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    busy.value = null
  }
}

const install = (v: string) => run(v, () => api.installPHP(v), `PHP ${v} downloaded`)
const remove = (v: string) => run(v, () => api.removePHP(v), `PHP ${v} removed`)
const update = (v: string) => run(v, async () => {
  const res = await api.updatePHP(v)
  if (res.error) message.warning(res.error)
  if (res.restarted.length) message.info(`Restarted: ${res.restarted.join(', ')}`)
  return res.php
}, `PHP ${v} updated`)

async function updateAll() {
  for (const v of updates.value) await update(v.version)
}
</script>

<template>
  <NModal v-model:show="show" preset="card" title="PHP versions" style="width: 820px; max-width: calc(100vw - 32px)">
    <div class="stack">
      <div class="row">
        <NButton :loading="checking" @click="check">
          <template #icon><NIcon :component="Refresh" /></template>
          Check for updates
        </NButton>
        <NButton v-if="updates.length" type="primary" :loading="!!busy" @click="updateAll">
          Update all ({{ updates.length }})
        </NButton>
        <span class="grow" />
        <span v-if="checkedAt" class="muted small">Checked at {{ checkedAt.toLocaleTimeString() }}</span>
      </div>

      <NAlert v-if="info?.error" type="error" :title="info.error" />

      <table class="versions">
        <thead>
          <tr><th>Version</th><th>Source</th><th>Status</th><th>Used by</th><th /></tr>
        </thead>
        <tbody>
          <tr v-for="v in info?.installed ?? []" :key="v.source + v.version">
            <td class="mono">{{ v.full || v.version }}</td>
            <td><NTag size="small" :type="v.source === 'system' ? 'default' : 'success'">{{ originLabel(v) }}</NTag></td>
            <td>
              <NTag v-if="v.update" size="small" type="warning">{{ v.latest === v.full ? 'new build available' : `${v.latest} available` }}</NTag>
              <span v-else-if="v.source === 'system'" class="muted small">updated with apt</span>
              <span v-else class="muted small">up to date</span>
            </td>
            <td>
              <span v-if="!v.used_by.length" class="muted small">—</span>
              <a v-for="id in v.used_by" :key="id" href="#" class="app-link" @click.prevent="emit('openApp', id)">{{ id }}</a>
            </td>
            <td class="right">
              <template v-if="v.source === 'laranest'">
                <NButton v-if="v.update" size="small" type="primary" :loading="busy === v.version" @click="update(v.version)">
                  Update
                </NButton>
                <NButton size="small" quaternary type="error" :disabled="busy === v.version" title="Remove" @click="remove(v.version)">
                  <template #icon><NIcon :component="Trash" /></template>
                </NButton>
              </template>
            </td>
          </tr>
          <tr v-for="r in info?.available ?? []" :key="'dl' + r.minor" class="downloadable">
            <td class="mono muted">{{ r.version }}</td>
            <td><span class="muted small">not installed</span></td>
            <td />
            <td />
            <td class="right">
              <NButton size="small" :loading="busy === r.minor" @click="install(r.minor)">
                <template #icon><NIcon :component="CloudDownload" /></template>
                Download
              </NButton>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="muted small">
        Laranest always uses its own builds (it does not depend on any PHP you have installed): they are downloaded for your user
        only, without administrator rights, and updating keeps their <code>php.ini</code> and restarts the apps that use them.
        Also from the terminal: <code>laranest php:list</code>, <code>php:update</code>.
      </div>
      <NAlert v-if="reducedSet" type="info" :show-icon="false">
        Using static-php's generic set (no <code>intl</code>, <code>opcache</code> or <code>sodium</code>) until Laranest's
        builds are available; they will then show up as an update.
      </NAlert>
    </div>
  </NModal>
</template>

<style scoped>
.versions { width: 100%; border-collapse: collapse; }
.versions th { text-align: left; font-weight: 500; color: var(--muted); font-size: 0.85rem; }
.versions th, .versions td { padding: 8px 6px; border-bottom: 1px solid var(--border); }
.right { text-align: right; white-space: nowrap; }
.app-link { color: var(--brand); margin-right: 8px; text-decoration: none; }
</style>
