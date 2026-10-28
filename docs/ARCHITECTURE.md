# Architecture

Laranest is a single Go binary that runs as a **systemd user service** (on
Windows, a background process started at sign-in; on macOS, a launchd agent). It serves a web panel, runs
each registered Laravel app with its own PHP-FPM pool (php-cgi workers on
Windows) and workers, and routes `https://<app>.localhost` to them through an
embedded Caddy. Everything runs as the current user: no root or administrator
rights, no containers. The Windows and macOS specifics are [below](#windows).

```
Browser (Windows or Linux) ──► embedded Caddy (127.0.0.1:80 / :443)
                                 ├─ laranest.localhost   → panel UI + API (also 127.0.0.1:9150)
                                 ├─ <app>.localhost      → php-fpm pool of the app (unix socket)
                                 └─ reverb-<app>.localhost → Reverb
                               systemd --user (laranest.slice)
                                 └─ laranest-<app>-{web,queue,horizon,scheduler,reverb,vite}.service
CLI `laranest` ─► same API, or runs PHP/Composer/Node directly
```

## Components

| Piece | Choice | Why |
|---|---|---|
| Daemon + CLI | Go, one static binary (`cmd/laranest`) | Trivial install, no dependency on the PHP it manages |
| Proxy / TLS | Caddy embedded as a library (`internal/proxy`) | Reverse proxy, FastCGI, local CA (`tls internal`) |
| Processes | Transient systemd user units (`internal/units`, go-systemd) | Restart with backoff, cgroup kills (no orphans), journald logs, apps survive daemon restarts |
| PHP | Static glibc builds made with static-php-cli (`php/`, `.github/workflows/php.yml`) | Same PHP on every machine, downloaded per user without sudo |
| UI | Vue 3 + TypeScript + Naive UI (`web/`), embedded with `go:embed` | Familiar to Laravel developers; built into the binary |
| State | JSON in `~/.config/laranest`, runtimes in `~/.local/share/laranest`, logs/CA in `~/.local/state/laranest` (Windows: `%LOCALAPPDATA%\Laranest\{config,data,state}`; `LARANEST_HOME` moves all three) | No external services |

## Running an app

- **web:** an unprivileged PHP-FPM pool per app (`php-fpm -c <php.ini> -y <conf> -F`),
  listening on `$XDG_RUNTIME_DIR/laranest/<app>.sock` (unix socket paths are
  limited to 108 bytes, so they don't live in the state dir).
- **queue / horizon, scheduler, reverb, vite:** optional per app, each its own
  unit. Laravel < 8 gets a `schedule:run` loop instead of `schedule:work`.
- Units get a `PATH` with Laranest's `php`/`composer` shims first, so tools
  that shell out to `php artisan` (laravel-vite-plugin, Wayfinder, Ziggy) use
  the project's PHP, not whatever the system has.
- Caddy serves existing files from `public/` and sends everything else to
  `index.php` over FastCGI. HTTPS apps redirect plain HTTP with a 308.

## PHP selection

1. The version pinned in the app settings, or else
2. the newest version allowed by **all** of: `require.php` in `composer.json`,
   the `php` requirement of every package in `composer.lock` (what Composer
   enforces), and the newest PHP the installed Laravel supports
   (5 → 7.4, 6/7 → 8.0, 8 → 8.1, 9 → 8.2, 10 → 8.3, 11 → 8.4).
3. If it isn't installed, it is downloaded from the `php` release (manifest +
   sha256), falling back to static-php-cli's generic set for versions or
   architectures Laranest doesn't build. System (apt) PHP is ignored unless
   `use_system_php` is set: every machine behaves the same.

Composer constraint syntax is normalized before matching (`|`, `,`, spaces,
and Composer's `~8.0` meaning `>=8.0 <9.0`).

## Detection and warnings

When an app is added (and on "Re-detect"): Laravel version, PHP requirement,
packages (Horizon, Reverb, Octane, Pulse, Livewire, Inertia), queue driver,
scheduled tasks (`schedule:list`) and Vite. That gives the recommended
processes.

On every view, fast checks run: `.env`/`APP_KEY`/`APP_URL`, permissions,
missing extensions (`ext-*` of `composer.json` and `composer.lock`), Vite CORS
and stale `public/hot`, Reverb variables. Slower checks are cached for 60 s and
open real connections with the `.env` credentials: MySQL/MariaDB/PostgreSQL/
SQLite (auth, missing database, unmigrated, missing driver tables), Redis
(auth, DB index, phpredis/predis client), Memcached, SMTP greeting,
Meilisearch/Typesense, S3 endpoints, `storage:link`, Docker/Sail hostnames, and
a request to the app's home page that surfaces the last logged exception on a
5xx.

Laranest never edits `.env` silently: fixes show a diff and need confirmation.

## HTTPS

Caddy's internal CA ("Laranest Local CA") issues certificates for
`*.localhost` on the fly. Trusting it (`laranest trust` or the panel button):

- **WSL, browsing from Windows:** `certutil.exe -user -addstore Root` — no admin
  rights; Windows asks for confirmation.
- **Ubuntu with local browsers:** the system store (sudo) plus the NSS
  databases of Chrome/Chromium and Firefox (deb and snap), via
  smallstep/truststore.

The apps' own PHP and Node trust it without touching the system: PHP gets
`-d openssl.cafile=<bundle>` and Node `NODE_EXTRA_CA_CERTS`.

## WSL specifics

- `*.localhost` resolves to loopback in browsers, so no hosts files are edited;
  WSL's localhost forwarding carries the traffic from Windows.
- WSL doesn't open logind sessions: the installer enables lingering first, or
  `systemctl --user` doesn't exist on a fresh distro.
- WSL stops a distro shortly after its last terminal closes, even with systemd
  services running. The panel offers (with confirmation) to set
  `instanceIdleTimeout=-1` in `%UserProfile%\.wslconfig`, and optionally a
  startup script that boots the distro at Windows sign-in.
- The npm that Windows adds to `PATH` (`/mnt/c/...`) is ignored.

## Windows

The same binary, built for Windows, replaces the Linux pieces it can't use:

| Linux | Windows |
|---|---|
| systemd user service + lingering | `laranest install` copies itself to `%LOCALAPPDATA%\Programs\Laranest`, adds that to the user PATH and registers `laranestw.exe` under `HKCU\…\Run`. `laranestw.exe` is a tiny GUI-subsystem launcher (embedded in `laranest.exe`) that starts `laranest serve` with no console window. |
| Transient systemd units | An in-process supervisor (`internal/units/supervisor.go`, shared with macOS): every process runs suspended → in its own **Job Object** (`KILL_ON_JOB_CLOSE`) → resumed, so stopping it kills its whole tree and nothing outlives the daemon, even if it crashes. Restart with the same 1s → 30s backoff; output to a log file and to the live logs. The apps stop with the daemon, and `Boot` starts them again. |
| PHP-FPM on a unix socket | `laranest cgi-pool`: 4 `php-cgi -b 127.0.0.1:<port>` workers per app (php-cgi serves one request at a time), restarted when they exit; Caddy load-balances them (`least_conn`, retrying while one restarts). |
| Static PHP builds (static-php-cli) | The official non-thread-safe x64 builds from windows.php.net (`releases.json`, sha256-checked), with every common extension enabled from `ext\` plus phpredis from PECL, and the Visual C++ runtime DLLs next to `php.exe` (`vcruntime-x64.zip` in the `php` release: a fresh Windows without the Redistributable works too). PHP's OpenSSL doesn't read the Windows store: the CA bundle is Mozilla's list (from curl.se) plus Laranest's CA. |
| `php` / `composer` symlinks | `php.exe` / `composer.exe` hard links to `laranest.exe` (symlinks need privileges). `execve` doesn't exist: the CLI runs the child on the same console and exits with its code. |
| `npm` from the Node bin dir | `node npm-cli.js`: npm/npx are `.cmd` scripts, which can't be started without a shell. |
| `laranest trust` via WSL interop | `certutil.exe -user -addstore Root` directly. |

Not available on Windows: Horizon (`pcntl`): `queue:work` runs instead, and
`ext-pcntl`/`ext-posix` requirements get a hint to install with
`--ignore-platform-req`. Vite is killed without a signal, so Laranest removes
the `public/hot` it leaves behind. Windows' socket errors are localized, so
port problems are explained from the error code (`WSAEADDRINUSE`,
`WSAEACCES` for ranges reserved by Hyper-V).

## macOS (beta)

macOS has no systemd either; it reuses the Windows supervisor and keeps the
rest of the Linux pieces:

| Linux | macOS |
|---|---|
| systemd user service + lingering | A launchd agent of the user, `~/Library/LaunchAgents/dev.laranest.plist` (`RunAtLoad`, restarted by launchd unless it exits cleanly), loaded with `launchctl bootstrap gui/<uid>` (`user/<uid>` without a desktop session). |
| Transient systemd units | The in-process supervisor. Each process leads a **process group** of its own: stopping it signals the whole group (SIGTERM, SIGKILL after 8s). Groups outlive their parent, so the running ones are recorded in `state/process-groups.json` with the boot time, and a daemon that starts after a crash kills the ones left behind. |
| PHP-FPM, logs to journald | The same PHP-FPM pool on a unix socket, logging to stderr, which the supervisor reads. |
| Static PHP builds | static-php-cli's macOS builds (`common` extension set), until the `php` release has macOS builds of its own (`manifest-macos.json`). |
| `laranest trust`: system store + NSS | The System Keychain (Safari, Chrome; macOS asks for your password), plus Firefox's profiles when `certutil` is installed (`brew install nss`). |
| `laranest setup-ports` | Not needed: macOS lets any user listen on 80/443. |

## Ports

Few ports are involved, and none is chosen per app for the web:

- **Panel** 9150 (the first free of 9150–9153 on the first run).
- **Proxy**: one HTTP and one HTTPS port for every app (80/443, else the
  first free of 8080…/8443/9443), routing by host name.
- **PHP**: a unix socket per app on Linux; on Windows 4 consecutive ports in
  19100–19999, probed when the app starts (skipping other apps', Reverb's and
  Hyper-V's reserved ranges) and released when it stops.
- **Reverb**: one port per app in 18080–18999, kept in `apps.json`; if
  another program took it, the app moves to a free one when it starts.
  Browsers reach it through the proxy (`reverb-<app>.localhost`), so `.env`
  keeps the proxy's port.
- **Vite** picks its own (5173 or the next free one) and writes it to
  `public/hot`. When `vite.config` pins a port with `strictPort` and another
  program holds it, the panel says so.

## Updates

`laranest update` and the panel's *Update* compare the SHA-256 of the
running binary with the latest release's `SHA256SUMS` (so a re-published
release, or a local build, is told apart too), download and verify the new
binary, and then:

- **Linux:** rename it over the installed one (atomic; running processes keep
  the old file) and `systemctl --user restart laranest`; the apps' units keep
  running.
- **macOS:** the same rename, then `launchctl kickstart -k`; the apps restart
  with the daemon.
- **Windows:** run its `install` detached, which stops the daemon (and the
  apps), replaces `%LOCALAPPDATA%\Programs\Laranest\laranest.exe` and starts
  the new one, which starts the apps again.

## MCP server

`laranest mcp` (`internal/mcpserver`, on the official Go SDK) speaks the Model
Context Protocol on stdio for AI clients. It is a thin client of the daemon's
HTTP API, so assistants get the panel's features with its validations: tools
to list, inspect, add, update (advanced settings included), start, stop,
restart and remove apps, read logs, set `.env` values, apply a warning's fix,
run php/composer/npm/npx/node commands and manage PHP versions. Tools carry
read-only/destructive hints so clients can ask before the risky ones.

## Security

The panel can run commands, so it listens on `127.0.0.1` only, rejects foreign
`Host` headers (DNS rebinding) and cross-origin mutating requests (CSRF).

## Releases

- Every push to main and pull request that touches code runs CI (Linux
  x64/ARM64: unit tests + an end-to-end smoke test; macOS ARM64/Intel: unit
  tests + an end-to-end test of the launchd install, `test/e2e/macos.sh`;
  Windows: unit tests + build) and the full Windows regression (`windows-e2e.yml`, ~15 min).
- `make publish RELEASE=vX.Y.Z` tags the current commit; the Release
  workflow waits for (or reads) that commit's CI and Windows regression on
  main — it doesn't test again, and a failed or missing run stops the
  release — then builds and publishes
  `laranest-{linux,darwin}-{amd64,arm64}`, `laranest-windows-amd64.exe`, `install.sh`,
  `install.ps1` and `SHA256SUMS`. Binaries embed the version and the commit
  (e.g. `v0.1.1+60680ba`).
- The PHP workflow rebuilds only the minors with a new patch on php.net (or
  everything when `php/extensions.txt` changes) and merges them into the
  rolling `php` release.

## Dependencies

Existing libraries and tools over custom code: Caddy, go-systemd, cobra,
Masterminds/semver, godotenv, smallstep/truststore, go-sql-driver/mysql, pgx,
go-redis, modernc.org/sqlite, static-php-cli, Naive UI, ansi_up, fuse.js.
PrimeVue was dropped because v5 requires a commercial license key.
