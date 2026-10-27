<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import Fuse from 'fuse.js'
import { NAlert, NAutoComplete, NButton, NIcon, NInput, NModal, NSelect, NSpin, NSwitch, NTag, useMessage } from 'naive-ui'
import { CheckmarkCircle, Folder, Play, Search } from '@vicons/ionicons5'
import ProcessToggles from './ProcessToggles.vue'
import GroupSelect from './GroupSelect.vue'
import { api, phpOptions, type Detection, type PHPInfo, type Processes, type System } from '../api'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ system: System | null; phpInfo: PHPInfo | null; groups: string[] }>()
const emit = defineEmits<{ added: [id: string]; settings: [] }>()
const message = useMessage()

const path = ref('')
const suggestions = ref<string[]>([])
const discovered = ref<{ path: string; registered: boolean }[]>([])
const discovering = ref(false)

const detection = ref<Detection | null>(null)
const inspectError = ref('')
const inspecting = ref(false)
const id = ref('')
const php = ref('')
const group = ref('')
const https = ref(true)
const processes = ref<Processes>({ queue: false, horizon: false, scheduler: false, reverb: false, vite: false })
const saving = ref(false)

const phpChoices = computed(() => phpOptions(props.phpInfo, 'Automatic (from composer.json)'))
const url = computed(() => {
  const port = props.system && props.system.http_port !== 80 ? `:${props.system.http_port}` : ''
  const httpsPort = props.system && props.system.https_port !== 443 ? `:${props.system.https_port}` : ''
  return https.value
    ? `https://${id.value || '…'}.${props.system?.domain ?? 'localhost'}${httpsPort}`
    : `http://${id.value || '…'}.${props.system?.domain ?? 'localhost'}${port}`
})

async function discover() {
  discovering.value = true
  try {
    discovered.value = await api.discover()
  } finally {
    discovering.value = false
  }
}

watch(show, (v) => {
  if (!v) return
  reset()
  discover()
})
// Scan dirs edited in Settings while this dialog is open.
watch(() => props.system?.scan_dirs?.join('\n'), () => show.value && discover())

// The input is both a path and a search box: paths get directory
// autocompletion, anything else fuzzy-filters the discovered projects.
const isPath = computed(() => /^[~/]/.test(path.value))
watch(path, async (p) => {
  suggestions.value = isPath.value && !detection.value ? await api.fs(p).catch(() => []) : []
})

const fuse = computed(() => new Fuse(discovered.value, {
  keys: [{ name: 'name', weight: 2 }, { name: 'path', weight: 1 }],
  ignoreLocation: true,
  threshold: 0.4,
  useExtendedSearch: true, // "coe demo" = both terms must match
  getFn: (d, key) => (key.toString() === 'name' ? d.path.split(/[\\/]/).pop()! : d.path),
}))
const filtered = computed(() => {
  const q = path.value.trim()
  if (!q || (isPath.value && discovered.value.some((d) => d.path === q))) return discovered.value
  // "~/dev/x" → "/dev/x", which every absolute path under home contains.
  if (isPath.value) return discovered.value.filter((d) => d.path.includes(q.replace(/^~/, '')))
  // Extended search: each space-separated word is fuzzy-matched and all must match.
  return fuse.value.search(q).map((r) => r.item)
})

// Enter: inspect the typed path, or the best search match.
function submit() {
  if (isPath.value || !filtered.value.length) inspect()
  else {
    const first = filtered.value.find((d) => !d.registered)
    if (first) inspect(first.path)
  }
}

function reset() {
  path.value = ''
  detection.value = null
  inspectError.value = ''
}

async function inspect(p = path.value) {
  if (!p) return
  path.value = p
  inspecting.value = true
  inspectError.value = ''
  detection.value = null
  try {
    const res = await api.inspect(p)
    if (res.error) {
      inspectError.value = res.error
      return
    }
    detection.value = res.detection
    id.value = res.suggested_id
    php.value = ''
    processes.value = { ...res.detection.recommended }
  } catch (e) {
    inspectError.value = (e as Error).message
  } finally {
    inspecting.value = false
  }
}

async function add(start: boolean) {
  if (!detection.value) return
  saving.value = true
  try {
    const res = await api.add({ path: detection.value.path, id: id.value, group: group.value, https: https.value, php: php.value, processes: processes.value, start })
    if (res.error) message.warning(`Added, but it did not start: ${res.error}`)
    else message.success(`App added: ${res.app.url}`)
    show.value = false
    emit('added', res.app.id)
  } catch (e) {
    message.error(`Could not add it: ${(e as Error).message}`)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" title="Add app" style="width: 640px; max-width: calc(100vw - 32px)">
    <div class="stack">
      <div class="row">
        <NAutoComplete v-model:value="path" :options="suggestions" placeholder="Search by name or type a path (~/projects/my-app)" class="grow"
          :input-props="{ class: 'mono', autocomplete: 'off' }" @keydown.enter="submit()" />
        <NButton type="primary" :loading="inspecting" @click="submit()">
          <template #icon><NIcon :component="Search" /></template>
          Inspect
        </NButton>
      </div>

      <template v-if="!detection">
        <NAlert v-if="inspectError" type="error">{{ inspectError }}</NAlert>
        <div>
          <div class="row section-title">
            <span>Laravel projects found</span>
            <span v-if="discovered.length" class="muted small">({{ filtered.length }}/{{ discovered.length }})</span>
          </div>
          <div class="row muted small scan-dirs">
            <NIcon :component="Folder" />
            <span class="mono grow">{{ system?.scan_dirs?.join(' · ') }}</span>
            <a href="#" @click.prevent="emit('settings')">Change folders</a>
          </div>
          <NSpin v-if="discovering" size="small" />
          <div v-else-if="!discovered.length" class="muted">No projects found. Type the path above or add search folders.</div>
          <div v-else-if="!filtered.length" class="muted">No project matches; Enter inspects the typed path.</div>
          <div v-else class="discovered">
            <button v-for="d in filtered" :key="d.path" class="discovered-item" :disabled="d.registered" @click="inspect(d.path)">
              <span class="mono small">{{ d.path }}</span>
              <NTag v-if="d.registered" size="small">already added</NTag>
            </button>
          </div>
        </div>
      </template>

      <template v-else>
        <div class="detected">
          <NIcon :component="CheckmarkCircle" color="#18a058" size="18" />
          <span>Laravel <strong>{{ detection.laravel_version }}</strong></span>
          <span v-if="detection.php_constraint" class="muted">PHP {{ detection.php_constraint }}</span>
          <NTag v-for="(_, pkg) in detection.packages" :key="pkg" size="small" type="info">{{ pkg }}</NTag>
        </div>

        <div class="grid2">
          <label class="field">
            <span>Name (subdomain)</span>
            <NInput v-model:value="id" :input-props="{ class: 'mono' }" />
            <small class="muted mono">{{ url }}</small>
          </label>
          <label class="field">
            <span>PHP version</span>
            <NSelect v-model:value="php" :options="phpChoices" />
          </label>
          <label class="field">
            <span>Group</span>
            <GroupSelect v-model="group" :groups="groups" />
          </label>
          <div class="field">
            <span>HTTPS</span>
            <div class="row">
              <NSwitch v-model:value="https" />
              <span class="clickable small muted" @click="https = !https">{{ https ? 'With Laranest\'s local certificate' : 'HTTP only' }}</span>
            </div>
          </div>
        </div>

        <div>
          <div class="section-title">Processes</div>
          <ProcessToggles v-model="processes" :detection="detection" />
        </div>
      </template>
    </div>

    <template v-if="detection" #action>
      <div class="row" style="justify-content: flex-end">
        <NButton quaternary @click="reset">Back</NButton>
        <NButton :loading="saving" @click="add(false)">Add without starting</NButton>
        <NButton type="primary" :loading="saving" @click="add(true)">
          <template #icon><NIcon :component="Play" /></template>
          Add and start
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.discovered { display: grid; gap: 4px; max-height: 260px; overflow: auto; }
.discovered-item {
  display: flex; justify-content: space-between; align-items: center; gap: 8px;
  padding: 8px 10px; border-radius: 6px; border: 1px solid var(--border);
  background: transparent; color: inherit; text-align: left; cursor: pointer;
}
.discovered-item:hover:not(:disabled) { border-color: var(--brand); }
.discovered-item:disabled { opacity: 0.6; cursor: default; }
.scan-dirs { margin-bottom: 8px; }
.scan-dirs a { color: var(--brand); white-space: nowrap; }
.detected { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
</style>
