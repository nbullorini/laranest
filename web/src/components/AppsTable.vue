<script setup lang="ts">
import { computed, h, ref, watch } from 'vue'
import { NButton, NDataTable, NIcon, type DataTableColumns } from 'naive-ui'
import { Play, Refresh, Stop, Warning } from '@vicons/ionicons5'
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

const problems = (app: AppView) => app.warnings.filter((w) => w.level !== 'info').length
const isUp = (app: AppView) => app.state !== 'stopped'

function iconButton(icon: typeof Play, title: string, loading: boolean, onClick: () => void, disabled = false) {
  return h(NButton, { quaternary: true, circle: true, size: 'small', title, loading, disabled,
    onClick: (e: MouseEvent) => { e.stopPropagation(); onClick() } },
  { icon: () => h(NIcon, { component: icon }) })
}

const columns = computed<DataTableColumns<Row>>(() => [
  {
    key: 'name',
    title: 'App',
    tree: true,
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
    width: 120,
    render: (row) => (row.isGroup ? null : h(StateTag, { state: row.app.state })),
  },
  {
    key: 'procs',
    title: 'Processes',
    render: (row) => row.isGroup ? null : h('div', { class: 'chips' }, row.app.procs.map((p) =>
      h('span', { class: ['chip', p.state], title: p.state }, [h('i', { class: 'dot' }), processLabels[p.name] ?? p.name]))),
  },
  {
    key: 'versions',
    title: 'PHP · Laravel',
    width: 140,
    render: (row) => row.isGroup ? null
      : h('span', { class: 'small' }, `${row.app.php_version || '?'} · ${row.app.detection?.laravel_version?.split('.')[0] ?? '?'}`),
  },
  {
    key: 'warnings',
    title: '',
    width: 60,
    render: (row) => {
      if (row.isGroup) return null
      const n = problems(row.app)
      return n ? h('span', { class: 'warn small row', title: `${n} warning(s)` }, [h(NIcon, { component: Warning }), String(n)]) : null
    },
  },
  {
    key: 'actions',
    title: '',
    width: 90,
    align: 'right',
    render: (row) => {
      if (row.isGroup) {
        const allUp = row.apps.every(isUp)
        return iconButton(allUp ? Stop : Play, allUp ? 'Stop the group' : 'Start the group', false,
          () => emit('group', row.apps, !allUp))
      }
      const up = isUp(row.app)
      return h('div', { class: 'row-actions' }, [
        iconButton(up ? Stop : Play, up ? 'Stop' : 'Start', props.busy === row.app.id, () => emit('toggle', row.app)),
        iconButton(Refresh, 'Restart', props.busy === `${row.app.id}:restart`, () => emit('restart', row.app), !up),
      ])
    },
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
  <NDataTable class="apps-table" :columns="columns" :data="rows" :row-key="(r: Row) => r.key" :row-props="rowProps"
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
.apps-table .link { color: var(--brand); text-decoration: none; }
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
.apps-table .row-actions { display: inline-flex; gap: 2px; }
</style>
