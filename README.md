<p align="center"><img src="docs/logo.png" alt="Laranest" width="160"></p>

# Laranest

[![CI](https://github.com/nbullorini/laranest/actions/workflows/ci.yml/badge.svg)](https://github.com/nbullorini/laranest/actions/workflows/ci.yml)

Run your existing Laravel projects locally with zero configuration. Point
Laranest at a project and it figures out what it needs, gives it its own PHP,
starts its workers and serves it at `https://<app>.localhost`.

- **No sudo or admin rights, no Docker, no system PHP.** Laranest downloads its
  own PHP (with the Laravel Forge/Herd extension set), Composer and Node per user.
- **Detects what each project needs:** Laravel and PHP versions, queues,
  Horizon, the scheduler, Reverb, Vite, Livewire/Inertia/Filament.
- **Runs it properly:** a PHP-FPM pool per app (php-cgi workers on Windows)
  plus queue/Horizon, scheduler, Reverb and Vite, restarted if they crash,
  with live logs.
- **The Laravel log, readable:** `storage/logs` in the panel, by level and
  searchable, each exception with its stack trace.
- **`dump()` and `dd()` in the panel** (optional, per app): every dump with the
  file and line that made it and the request or command it came from.
- **Xdebug per app**, only where you turn it on: step debugging on a trigger
  (your browser's Xdebug helper, or `XDEBUG_TRIGGER`), with your IDE on port
  9003. Other apps and processes run without it, at full speed.
- **`laranest.json` for teams:** save an app's setup (PHP version, processes,
  custom commands) to the project, commit it, and everyone who adds the project
  gets the same setup.
- **Share an app** with a public link (a free Cloudflare quick tunnel, no
  account) to show it to a client or test a webhook; Laravel builds its URLs
  with that address.
- **HTTPS by default** with a local certificate authority your browsers trust.
- **Tells you what is wrong:** missing `.env` values, an invalid `APP_KEY`,
  databases or Redis that reject the credentials, unmigrated databases,
  missing PHP extensions, Vite CORS issues, a 500 with its exception, and more.
  Many come with a one-click fix: a diff before touching `.env`, or the
  command that fixes it (`npm install`, `php artisan migrate`…) to run from
  the panel.

Runs on **Linux** (any distribution with systemd), **WSL2** (browsing from
Windows), **Windows 10/11** and, in beta, **macOS**, on x86_64 and ARM64. See
[supported platforms](#supported-platforms).

## Install

**Linux, WSL2 and macOS:**

```bash
curl -fsSL https://github.com/nbullorini/laranest/releases/latest/download/install.sh | bash
```

**Windows** (PowerShell, no administrator rights):

```powershell
irm https://github.com/nbullorini/laranest/releases/latest/download/install.ps1 | iex
```

Then open **http://localhost:9150** (the installer prints the address: it
uses the next free port if 9150 is taken, for example by a Laranest running
inside WSL).

Requirements:

- **Linux / WSL2:** a distribution with systemd (on WSL2, `[boot]
  systemd=true` in `/etc/wsl.conf`, the default on current Ubuntu images). Run it as your normal
  user, not root: it puts `laranest` in `~/.local/bin` and starts it as a
  systemd user service, without sudo. On WSL, if your distro logs in as
  `root`, create a user first (`adduser <name>`), set `[user] default=<name>`
  in `/etc/wsl.conf` and restart the distro.
- **macOS (beta):** macOS 13 or later, Apple silicon or Intel. Same command,
  as your normal user: `laranest` goes into `~/.local/bin` and starts as a
  launchd agent of your user (`~/Library/LaunchAgents/dev.laranest.plist`),
  without administrator rights.
- **Windows:** 64-bit Windows 10 or 11. Laranest installs into
  `%LOCALAPPDATA%\Programs\Laranest`, adds itself to your PATH and starts in the
  background when you sign in (no window, no administrator rights).

### Supported platforms

| | Platforms |
|---|---|
| **Tested** (CI on every change, or by hand) | Ubuntu 24.04 (x86_64, ARM64) and 22.04 · WSL2 with Ubuntu 24.04 · Windows 11 · Windows Server 2025 |
| **Supported** (same requirements, not tested on every change) | Debian 11, 12, 13 · Ubuntu 20.04+ · RHEL, Rocky and Alma 8, 9, 10 · CentOS Stream 9, 10 · Fedora · Arch, Manjaro, openSUSE, Linux Mint, Pop!_OS · WSL2 with any of them · Windows 10 (64-bit) · Windows on ARM64 (x64 emulation) |
| **Beta** (waiting for reports from Mac users) | macOS 15 on Intel (CI on every change) and Apple silicon (tested before releases) · macOS 13 and 14 |
| **Not supported** | CentOS 7 and Ubuntu 18.04 (systemd and glibc too old) · distributions without systemd or glibc (Alpine, Void, Devuan, Gentoo with OpenRC) · WSL1 · 32-bit Windows |

On Linux, Laranest needs systemd 236+ with user sessions and glibc 2.28+ (for
the Node.js it downloads; its PHP builds need only 2.17). On Windows, PHP gets
the Visual C++ runtime it needs next to it, so nothing has to be installed
system-wide.

### Optional, one-time steps

| | |
|---|---|
| **Trust the HTTPS certificates** | Click *Trust on Windows…* in the panel (Windows and WSL; Windows asks you to confirm) or run `laranest trust` (Ubuntu: asks for your sudo password once; also covers Chrome and Firefox. macOS: adds it to the System Keychain, used by Safari and Chrome, asking for your password). |
| **Clean URLs (ports 80/443)** | Linux: `laranest setup-ports` (asks for your sudo password once). Windows needs nothing: Laranest uses 80/443 when they are free. macOS only lets root use them on 127.0.0.1, so it uses 8080/8443 for now. Otherwise it picks free ports such as 8080/8443. |
| **WSL: keep it running** | WSL stops a distro shortly after its last terminal closes. The panel offers to set `instanceIdleTimeout=-1` in your `.wslconfig` and, optionally, to start Laranest when you sign in to Windows. |

## Using it

1. **Add app** → pick one of the Laravel projects found in your home folder (or
   type a path). Laranest shows the detected versions and recommended processes.
2. **Add and start.** If the project needs a PHP version you don't have, it is
   downloaded on the fly.
3. Open `https://<app>.localhost`. Warnings appear on the app with a hint,
   and a *Fix .env* or *Run* button when Laranest can fix them for you.

Each app has a **Commands** tab: type `php artisan migrate`, `composer
install`, `npm run build`… (no need for `laranest` in front) and it runs in the
project's folder with the project's PHP and Node, as `laranest …` would in a
terminal. Its output and every command run from the panel are kept as a
history.

Apps can be grouped (and started/stopped as a group), renamed, switched
between HTTP and HTTPS, pinned to a PHP version, and their processes toggled.
The **PHP** view lists every version, checks for updates and applies them
(keeping your `php.ini` and restarting the apps that use it).

Laranest does not start databases, Redis or mail servers: it connects to the
ones your `.env` points at and tells you when they are down or misconfigured.

### Advanced: your own commands and processes

When the automatic setup isn't enough, *Settings → Advanced* lets you:

- **change the command** of a built-in process (e.g. `php artisan queue:work --queue=high,default`);
- **add processes**: anything the project needs running (`php artisan pulse:check`,
  `stripe listen --forward-to {url}/stripe/webhook`…), supervised and with logs like the others;
- **serve the web through a port** instead of PHP-FPM/php-cgi, for Laravel Octane or
  a custom server: `php artisan octane:start --port={port}` (Laranest picks the port
  and keeps the app's HTTPS URL).

`php`, `composer`, `npm`, `npx` and `node` in those commands are the project's own.
Placeholders: `{url}`, `{path}`, `{reverb_port}` and, for the web, `{port}`.

### Use it from AI assistants (MCP)

Laranest includes an [MCP](https://modelcontextprotocol.io) server, so Claude,
Cursor, VS Code or any MCP client can manage your apps: find and add projects,
read and change their settings (advanced ones too), start and stop them, read
their logs, fix their warnings and run commands in them. It talks to the
running Laranest, with the same rules as the panel.

```bash
claude mcp add -s user laranest -- laranest mcp          # Claude Code
```

Other clients take a config like this one (the panel's **MCP** button shows
it with the full path to `laranest`, which desktop apps may need, and, on WSL,
the variant for AI apps running on Windows):

```json
{ "mcpServers": { "laranest": { "command": "laranest", "args": ["mcp"] } } }
```

### Commands in your project

Use the project's own PHP from its folder:

```bash
laranest php artisan migrate
laranest composer install
laranest npm run build          # npm/npx/node; `php` inside Node tools is the project's PHP too
laranest which-php
```

`laranest shims enable` makes plain `php` and `composer` do the same in new
terminals (`laranest shims disable` reverts it). On Windows it adds them to
your user PATH.

### Team setup: `laranest.json`

*Settings → Save to laranest.json* writes the app's setup to the project.
Commit it: when someone adds the project, Laranest starts from it, and when it
changes (after a `git pull`) it is applied again on the next start or
re-detect. Everything is optional; what is missing stays automatic:

```json
{
  "php": "8.4",
  "https": true,
  "processes": { "queue": true, "scheduler": true, "vite": true },
  "commands": { "queue": "php artisan queue:work --queue=high,default" },
  "custom": [{ "name": "pulse", "command": "php artisan pulse:check" }],
  "web_mode": ""
}
```

The app's name, group, dumps and Xdebug stay on each machine. Changes made in
the panel stay local until the file changes, or until you save them to it.

### On Windows

- PHP comes from the official [windows.php.net](https://windows.php.net) builds
  (with phpredis from PECL), and each app is served by a few `php-cgi` workers,
  as Windows has no PHP-FPM.
- Horizon can't run on Windows (it needs the Unix-only `pcntl` extension):
  Laranest runs `queue:work` for its queues instead. Projects that require
  `ext-pcntl`/`ext-posix` install with
  `laranest composer install --ignore-platform-req=ext-pcntl --ignore-platform-req=ext-posix`.
- The apps stop with Laranest and start again with it (at sign-in, or
  `laranest install`).
- Use WSL instead when a project depends on Unix tooling.

### CLI reference

| Command | |
|---|---|
| `laranest status` | Service and panel status |
| `laranest update` | Update Laranest to the latest release |
| `laranest mcp` | MCP server (stdio) for AI assistants |
| `laranest php …` / `composer …` / `npm …` / `npx …` / `node …` | Run with the current project's runtimes |
| `laranest which-php` | Which PHP the current folder uses |
| `laranest php:list` / `php:install <v>` / `php:update [v]` / `php:remove <v>` | Manage PHP versions |
| `laranest trust [--status]` | Trust the local CA (the Windows user store on Windows and WSL; the system + browsers on Linux; the System Keychain on macOS) |
| `laranest setup-ports` | Allow ports 80/443 without root (Linux) |
| `laranest shims enable` / `disable` | Plain `php`/`composer` per project |
| `laranest install` / `uninstall` | Install or remove the background service (systemd user unit; on macOS, a launchd agent; on Windows, the sign-in entry) |
| `laranest serve` | Run the daemon in the foreground |

### Updating

The panel tells you when a new Laranest is published (*Update now*, or
*Settings → Check for updates*); from a terminal, `laranest update`. Running
the installer again does the same. Your apps, settings and PHP versions are
kept: on Linux the apps keep running meanwhile, on Windows and macOS they restart
with Laranest.

### Uninstall

On Linux and macOS:

```bash
laranest uninstall        # stops every app and removes the service
rm -rf ~/.local/bin/laranest ~/.config/laranest ~/.local/share/laranest ~/.local/state/laranest
```

On Windows:

```powershell
laranest uninstall        # stops every app, removes the sign-in entry and the PATH entries
Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\Laranest", "$env:LOCALAPPDATA\Laranest"
```

Your projects are never modified, except for `.env` changes you confirm.

## How it works

A single Go binary with an embedded web UI, an embedded Caddy (reverse proxy,
FastCGI and a local CA) and systemd user units for every process (on Windows
and macOS the daemon supervises them itself). See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the details: PHP selection,
the checks behind each warning, HTTPS trust on WSL and Ubuntu, and releases.

## Development

Requirements: Go (see `go.mod`) and Node 20+.

```bash
make build       # UI (web/dist) + bin/laranest
make windows     # bin/laranest.exe (cross-compiled)
make dev         # daemon on :9150 + Vite with hot reload on :5173
make install     # build, copy to ~/.local/bin and (re)install the service
make test        # go test ./...
make publish RELEASE=v0.1.2   # tag the current commit and publish it (see docs/ARCHITECTURE.md)
```

End-to-end tests (isolated instances, never touching your real setup), on
Linux and on Windows: regression suites with 100+ checks, integration tests
against real MySQL, PostgreSQL, Redis and Mailpit, and installs of 20 open
source Laravel projects.
See [test/e2e/README.md](test/e2e/README.md) and the latest
[results](test/e2e/RESULTS.md).

## License

[GNU AGPL v3](LICENSE). You can use Laranest freely, including at work, and
modify it; if you distribute a modified version, or offer it to others over a
network, its source must be published under the same license.
