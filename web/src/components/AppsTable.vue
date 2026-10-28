<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref, watch, type VNode } from 'vue'
import { NButton, NDataTable, NIcon, type DataTableColumns } from 'naive-ui'
import { ChevronDown, ChevronForward, Play, Refresh, Stop, Warning } from '@vicons/ionicons5'
import StateTag from './StateTag.vue'
import { processLabels, type AppView } from '../api'

const props = defineProps<{ apps: AppView[]; busy: string | null }>()
const emit = defineEmits<{ open: [id: string]; toggle: [app: AppView]; restart: [app: AppView]; group: [apps: AppView[], start: boolean] }>()

const NO_GROUP = 'No group'

interface GroupRow {
  key: string
  isGroup: true
  name: string
  apps: AppView[]
  children: AppRow[]
}
interface AppRow {
  key: string
  isGroup: false
  app: AppView
}
type Row = GroupRow | AppRow

const grouped = computed(() => props.apps.some((a) => a.group))

const rows = computed<Row[]>(() => {
  const toRow = (app: AppView): AppRow => ({ key: 'app:' + app.id, isGroup: false, app })
  if (!grouped.value) return props.apps.map(toRow)
  const byGroup = new Map<string, AppView[]>()
  for (const app of props.apps) {
    const g = app.group || NO_GROUP
    byGroup.set(g, [...(byGroup.get(g) ?? []), app])
  }
  const names = [...byGroup.keys()].sort((a, b) =>
    a === NO_GROUP ? 1 : b === NO_GROUP ? -1 : a.localeCompare(b))
  return names.map((name) => {
    const apps = byGroup.get(name)!
    return { key: 'group:' + name, isGroup: true, name, apps, children: apps.map(toRow) }
  })
})

// Collapsed groups are remembered per browser.
const STORAGE_KEY = 'laranest.collapsedGroups'
function loadCollapsed(): string[] {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]')
  } catch {
    return []
  }
}
const collapsed = ref<string[]>(loadCollapsed())
const expandedKeys = computed(() =>
  rows.value.filter((r) => r.isGroup && !collapsed.value.includes(r.key)).map((r) => r.key))
function onExpand(keys: (string | number)[]) {
  collapsed.value = rows.value.filter((r) => r.isGroup && !keys.includes(r.key)).map((r) => r.key)
}
watch(collapsed, (v) => {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(v))
  } catch { /* private mode: just don't remember */ }
})

// Phones and tablets get cards instead of the table: its columns don't fit.
const narrowQuery = window.matchMedia('(max-width: 980px)')
const narrow = ref(narrowQuery.matches)
const onNarrow = (e: MediaQueryListEvent) => (narrow.value = e.matches)
narrowQuery.addEventListener('change', onNarrow)
onBeforeUnmount(() => narrowQuery.removeEventListener('change', onNarrow))

// Renders VNodes built by the helpers below inside the template.
const VNodes = (p: { v: VNode | VNode[] | null }) => p.v

const problems = (app: AppView) => app.warnings.filter((w) => w.level !== 'info').length
const isUp = (app: AppView) => app.state !== 'stopped'

function iconButton(icon: typeof Play, title: string, loading: boolean, onClick: () => void, disabled = false) {
  return h(NButton, { quaternary: true, circle: true, size: 'small', title, loading, disabled,
    onClick: (e: MouseEvent) => { e.stopPropagation(); onClick() } },
  { icon: () => h(NIcon, { component: icon }) })
}

function appActions(app: AppView) {
  const up = isUp(app)
  return h('div', { class: 'row-actions' }, [
    iconButton(up ? Stop : Play, up ? 'Stop' : 'Start', props.busy === app.id, () => emit('toggle', app)),
    iconButton(Refresh, 'Restart', props.busy === `${app.id}:restart`, () => emit('restart', app), !up),
  ])
}

function groupActions(row: GroupRow) {
  const allUp = row.apps.every(isUp)
  return iconButton(allUp ? Stop : Play, allUp ? 'Stop the group' : 'Start the group', false,
    () => emit('group', row.apps, !allUp))
}

const chips = (app: AppView) => h('div', { class: 'chips' }, app.procs.map((p) =>
  h('span', { class: ['chip', p.state], title: p.state }, [h('i', { class: 'dot' }), processLabels[p.name] ?? p.name])))

function warningsBadge(app: AppView) {
  const n = problems(app)
  return n ? h('span', { class: 'warn small row', title: `${n} warning(s)` }, [h(NIcon, { component: Warning }), String(n)]) : null
}

const versions = (app: AppView) =>
  `${app.php_version || '?'} · ${app.detection?.laravel_version?.split('.')[0] ?? '?'}`

const isExpanded = (row: GroupRow) => expandedKeys.value.includes(row.key)
function toggleExpanded(row: GroupRow) {
  onExpand(isExpanded(row) ? expandedKeys.value.filter((k) => k !== row.key) : [...expandedKeys.value, row.key])
}

const columns = computed<DataTableColumns<Row>>(() => [
  {
    key: 'name',
    title: 'App',
    tree: true,
    minWidth: 190,
    render: (row) => {
      if (row.isGroup) {
        const up = row.apps.filter(isUp).length
        return h('span', { class: 'group-name' }, [row.name, h('span', { class: 'muted small' }, ` · ${up}/${row.apps.length} running`)])
      }
      return h('div', { class: 'app-cell' }, [
        h('span', { class: 'app-id' }, row.app.id),
        h('a', { href: row.app.url, target: '_blank', class: 'mono small link', onClick: (e: MouseEvent) => e.stopPropagation() }, row.app.url),
      ])
    },
  },
  {
    key: 'state',
    title: 'Status',
    width: 110,
    render: (row) => (row.isGroup ? null : h(StateTag, { state: row.app.state })),
  },
  {
    key: 'procs',
    title: 'Processes',
    minWidth: 220,
    render: (row) => (row.isGroup ? null : chips(row.app)),
  },
  {
    key: 'versions',
    title: 'PHP · Laravel',
    width: 140,
    render: (row) => (row.isGroup ? null : h('span', { class: 'small' }, versions(row.app))),
  },
  {
    key: 'warnings',
    title: '',
    width: 48,
    render: (row) => (row.isGroup ? null : warningsBadge(row.app)),
  },
  {
    key: 'actions',
    title: '',
    width: 90,
    align: 'right',
    render: (row) => (row.isGroup ? groupActions(row) : appActions(row.app)),
  },
])

const rowProps = (row: Row) => ({
  class: row.isGroup ? 'group-row' : 'app-row',
  onClick: () => (row.isGroup ? onExpand(expandedKeys.value.includes(row.key)
    ? expandedKeys.value.filter((k) => k !== row.key)
    : [...expandedKeys.value, row.key]) : emit('open', row.app.id)),
})
</script>

<template>
  <div v-if="narrow" class="apps-table apps-cards">
    <template v-for="row in rows" :key="row.key">
      <div v-if="row.isGroup" class="group-row card-group" @click="toggleExpanded(row)">
        <NIcon :component="isExpanded(row) ? ChevronDown : ChevronForward" />
        <span class="group-name grow">{{ row.name }}
          <span class="muted small">· {{ row.apps.filter(isUp).length }}/{{ row.apps.length }} running</span></span>
        <VNodes :v="groupActions(row)" />
      </div>
      <div v-for="r in row.isGroup ? (isExpanded(row) ? row.children : []) : [row]" :key="r.key"
        class="app-row app-card" :class="{ nested: row.isGroup }" @click="emit('open', (r as AppRow).app.id)">
        <div class="card-top">
          <div class="app-cell grow">
            <span class="app-id">{{ (r as AppRow).app.id }}</span>
            <a :href="(r as AppRow).app.url" target="_blank" class="mono small link" @click.stop>{{ (r as AppRow).app.url }}</a>
          </div>
          <VNodes :v="appActions((r as AppRow).app)" />
        </div>
        <VNodes :v="chips((r as AppRow).app)" />
        <div class="card-meta small">
          <StateTag :state="(r as AppRow).app.state" />
          <span class="muted">PHP · Laravel {{ versions((r as AppRow).app) }}</span>
          <span class="grow" />
          <VNodes :v="warningsBadge((r as AppRow).app)" />
        </div>
      </div>
    </template>
  </div>
  <NDataTable v-else class="apps-table" :columns="columns" :data="rows" :row-key="(r: Row) => r.key" :row-props="rowProps"
    :expanded-row-keys="expandedKeys" :indent="20" :bordered="false" size="small"
    @update:expanded-row-keys="onExpand" />
</template>

<!-- Not scoped: cells are rendered with h() inside NDataTable, which scoped CSS doesn't reach. -->
<style>
.apps-table .app-row, .apps-table .group-row { cursor: pointer; }
.apps-table .group-row td { background: var(--chip) !important; }
.apps-table .group-name { font-weight: 600; }
.apps-table .group-name .muted { font-weight: 400; }
.apps-table .app-cell { display: inline-flex; flex-direction: column; vertical-align: middle; line-height: 1.35; }
.apps-table .app-id { font-weight: 600; }
.apps-table .app-cell { max-width: 100%; min-width: 0; }
.apps-table:not(.apps-cards) .app-cell { max-width: 300px; }
.apps-table .link {
  color: var(--brand); text-decoration: none; display: block;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.apps-table .link:hover { text-decoration: underline; }
.apps-table .chips { display: flex; flex-wrap: wrap; gap: 4px; }
.apps-table .chip {
  display: inline-flex; align-items: center; gap: 5px; font-size: 0.75rem; padding: 1px 7px;
  border-radius: 999px; background: var(--chip);
}
.apps-table .dot { width: 7px; height: 7px; border-radius: 50%; background: #8a8a96; }
.apps-table .chip.running .dot { background: #18a058; }
.apps-table .chip.backoff .dot, .apps-table .chip.starting .dot { background: #f0a020; }
.apps-table .chip.failed .dot { background: #d03050; }
.apps-table .warn { color: #f0a020; gap: 3px; }
.apps-table .row-actions { display: inline-flex; gap: 2px; flex-shrink: 0; }

/* Phones and tablets: one card per app. */
.apps-cards { display: flex; flex-direction: column; }
.apps-cards .card-group { display: flex; align-items: center; gap: 8px; padding: 10px 6px; border-radius: 8px; background: var(--chip); margin-top: 4px; }
.apps-cards .app-card { display: flex; flex-direction: column; gap: 8px; padding: 12px 6px; border-bottom: 1px solid var(--border); }
.apps-cards .app-card:last-child { border-bottom: none; }
.apps-cards .app-card.nested { padding-left: 16px; }
.apps-cards .card-top { display: flex; align-items: flex-start; gap: 8px; }
.apps-cards .card-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
</style>
