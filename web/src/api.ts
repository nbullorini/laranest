export interface Processes {
  queue: boolean
  horizon: boolean
  scheduler: boolean
  reverb: boolean
  vite: boolean
}

export interface Detection {
  path: string
  is_laravel: boolean
  name: string
  laravel_version: string
  php_constraint: string
  php_max?: string // newest PHP the project's Laravel supports
  packages: Record<string, boolean>
  uses_vite: boolean
  has_env: boolean
  has_vendor: boolean
  scheduled_tasks: number
  recommended: Processes
}

export type ProcState = 'stopped' | 'starting' | 'running' | 'backoff' | 'stopping' | 'failed'

export interface ProcStatus {
  name: string
  unit: string
  state: ProcState
  pid?: number
  restarts: number
  started_at?: string
}

export interface Warning {
  level: 'error' | 'warning' | 'info'
  code: string
  message: string
  hint?: string
  /** A laranest command that fixes it, without "laranest" (e.g. "npm install"). */
  command?: string
  fix?: Record<string, string>
  current?: Record<string, string>
}

/** Whether a newer Laranest is published. */
export interface UpdateStatus {
  current: string
  available: boolean
  checked_at?: string
  error?: string
}

/** A command run from the panel (`laranest <line>` in the app's folder). */
export interface Command {
  id: string
  line: string
  args: string[]
  status: 'running' | 'ok' | 'failed' | 'stopped'
  exit_code?: number
  started_at: string
  finished_at?: string
  /** The warning a Run button started it for, and whether it is still there. */
  fixes?: string
  unresolved?: boolean
  output: string
}

export interface AppView {
  id: string
  name: string
  group: string
  https: boolean
  path: string
  php: string
  processes: Processes
  reverb_port: number
  detection: Detection | null
  url: string
  reverb_url?: string
  state: 'running' | 'partial' | 'stopped'
  php_version: string
  php_source: 'system' | 'laranest' | ''
  procs: ProcStatus[]
  warnings: Warning[]
  /** Advanced: command overrides, extra processes and the web mode. */
  commands?: Record<string, string>
  custom?: CustomProcess[]
  web_mode?: '' | 'port'
  /** What each built-in process runs unless overridden. */
  default_commands: Record<string, string>
}

export interface CustomProcess {
  name: string
  command: string
  disabled?: boolean
}

export interface Runtime {
  version: string
  full?: string
  source: 'system' | 'laranest'
  fpm: string
  cli: string
}

export interface PHPRelease {
  version: string
  minor: string
  source: 'laranest' | 'static-php'
}

export interface PHPVersion extends Runtime {
  full: string
  origin?: 'laranest' | 'static-php'
  latest?: string
  update: boolean
  used_by: string[]
}

export interface PHPInfo {
  installed: PHPVersion[]
  available: PHPRelease[]
  installing: string[]
  error?: string
}

export interface System {
  version: string
  executable: string
  os: 'linux' | 'windows'
  data_dir: string
  panel_url: string
  domain: string
  http_port: number
  proxy_error?: string
  proxy_hint?: string
  runtimes: Runtime[]
  cli_only: string[] | null
  scan_dirs: string[]
  installing: string[]
  https_port: number
  tls_error?: string
}

export interface TrustStatus {
  ready: boolean
  subject?: string
  system: boolean
  windows?: boolean // on Windows, and on WSL when the Windows profile is known
  nss_tool: boolean
}

export interface WSLStatus {
  is_wsl: boolean
  supported: boolean
  distro: string
  config_file: string
  keep_alive: boolean
  autostart: boolean
}

export interface LogLine {
  time: string
  stream: 'out' | 'sys'
  text: string
}

/** Result of an action that may succeed partially (e.g. added but failed to start). */
export interface AppResult {
  app: AppView
  error?: string
}

export class ApiError extends Error {}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(data.error ?? `${res.status} ${res.statusText}`)
  return data as T
}

export const api = {
  system: () => request<System>('GET', '/system'),
  health: () => request<{ status: string; version: string }>('GET', '/health'),
  checkUpdate: (refresh = false) => request<UpdateStatus>('GET', `/update${refresh ? '?refresh=1' : ''}`),
  updateLaranest: () => request<{ status: string }>('POST', '/update'),
  updateSystem: (patch: { http_port?: number; https_port?: number; scan_dirs?: string[] }) => request<System>('PATCH', '/system', patch),
  discover: () => request<{ path: string; registered: boolean }[]>('GET', '/discover'),
  fs: (prefix: string) => request<string[]>('GET', `/fs?prefix=${encodeURIComponent(prefix)}`),

  trust: () => request<TrustStatus>('GET', '/trust'),
  trustWindows: () => request<TrustStatus>('POST', '/trust/windows'),
  wsl: () => request<WSLStatus>('GET', '/wsl'),
  setWSL: (patch: { keep_alive?: boolean; autostart?: boolean }) => request<WSLStatus>('PATCH', '/wsl', patch),
  php: (refresh = false) => request<PHPInfo>('GET', refresh ? '/php?refresh=1' : '/php'),
  updatePHP: (version: string) =>
    request<{ php: PHPInfo; restarted: string[]; error?: string }>('POST', `/php/${version}/update`),
  installPHP: (version: string) => request<PHPInfo>('POST', `/php/${version}`),
  removePHP: (version: string) => request<PHPInfo>('DELETE', `/php/${version}`),

  apps: () => request<AppView[]>('GET', '/apps'),
  inspect: (path: string) =>
    request<{ detection: Detection; suggested_id: string; error?: string }>('POST', '/inspect', { path }),
  add: (req: { path: string; id: string; group: string; https: boolean; php: string; processes: Processes; start: boolean }) =>
    request<AppResult>('POST', '/apps', req),
  update: (id: string, patch: {
    id?: string; group?: string; https?: boolean; php?: string; processes?: Processes
    commands?: Record<string, string>; custom?: CustomProcess[]; web_mode?: string
  }) =>
    request<AppResult>('PATCH', `/apps/${id}`, patch),
  remove: (id: string) => request<void>('DELETE', `/apps/${id}`),
  start: (id: string) => request<AppResult>('POST', `/apps/${id}/start`),
  stop: (id: string) => request<AppResult>('POST', `/apps/${id}/stop`),
  restart: (id: string, process = '') => request<AppResult>('POST', `/apps/${id}/restart`, { process }),
  redetect: (id: string) => request<AppResult>('POST', `/apps/${id}/detect`),
  setEnv: (id: string, values: Record<string, string>) =>
    request<AppResult>('POST', `/apps/${id}/env`, { values }),
  logsURL: (id: string, process: string) => `/api/apps/${id}/logs/${process}`,
  commands: (id: string) => request<Command[]>('GET', `/apps/${id}/commands`),
  runCommand: (id: string, line: string, fixes = '') => request<Command>('POST', `/apps/${id}/commands`, { line, fixes }),
  command: (id: string, cmd: string) => request<Command>('GET', `/apps/${id}/commands/${cmd}`),
  stopCommand: (id: string, cmd: string) => request<void>('POST', `/apps/${id}/commands/${cmd}/stop`),
}

export const processLabels: Record<string, string> = {
  web: 'Web (PHP)',
  queue: 'Queue worker',
  horizon: 'Horizon',
  scheduler: 'Scheduler',
  reverb: 'Reverb',
  vite: 'Vite (dev)',
}

/** PHP versions for a selector: installed first, then downloadable ones. */
export function phpOptions(info: PHPInfo | null, autoLabel: string) {
  const installed = new Set((info?.installed ?? []).map((r) => r.version))
  return [
    { label: autoLabel, value: '' },
    ...(info?.installed ?? []).map((r) => ({
      label: `PHP ${r.full || r.version} · ${r.source === 'system' ? 'system' : 'Laranest'}`,
      value: r.version,
    })),
    ...(info?.available ?? []).filter((r) => !installed.has(r.minor)).map((r) => ({
      label: `PHP ${r.version} · downloaded on start`,
      value: r.minor,
    })),
  ]
}

/** Whether the browser the user browses with trusts Laranest's HTTPS. */
export function browserTrusts(t: TrustStatus | null, wsl: WSLStatus | null) {
  if (!t?.ready) return true // nothing to trust yet
  if (t.windows !== undefined) return t.windows
  return wsl?.is_wsl ? false : t.system
}
