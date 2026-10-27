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
- **HTTPS by default** with a local certificate authority your browsers trust.
- **Tells you what is wrong:** missing `.env` values, an invalid `APP_KEY`,
  databases or Redis that reject the credentials, unmigrated databases,
  missing PHP extensions, Vite CORS issues, a 500 with its exception, and more.
  Many come with a one-click fix: a diff before touching `.env`, or the
  command that fixes it (`npm install`, `php artisan migrate`…) to run from
  the panel.

Runs on **Windows 10/11**, **Ubuntu** and **WSL2** (browsing from Windows), on
x86_64 and ARM64.

## Install

**Linux and WSL2:**

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

- **Linux / WSL2:** systemd (Ubuntu 22.04+; on WSL2, `[boot] systemd=true` in
  `/etc/wsl.conf`, the default on current Ubuntu images). Run it as your normal
  user, not root: it puts `laranest` in `~/.local/bin` and starts it as a
  systemd user service, without sudo. On WSL, if your distro logs in as
  `root`, create a user first (`adduser <name>`), set `[user] default=<name>`
  in `/etc/wsl.conf` and restart the distro.
- **Windows:** 64-bit Windows 10 or 11. Laranest installs into
  `%LOCALAPPDATA%\Programs\Laranest`, adds itself to your PATH and starts in the
  background when you sign in (no window, no administrator rights).

### Optional, one-time steps

| | |
|---|---|
| **Trust the HTTPS certificates** | Click *Trust on Windows…* in the panel (Windows and WSL; Windows asks you to confirm) or run `laranest trust` (Ubuntu: asks for your sudo password once; also covers Chrome and Firefox). |
| **Clean URLs (ports 80/443)** | Linux: `laranest setup-ports` (asks for your sudo password once). Windows needs nothing: Laranest uses 80/443 when they are free. Otherwise it picks free ports such as 8080/8443. |
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
| `laranest php …` / `composer …` / `npm …` / `npx …` / `node …` | Run with the current project's runtimes |
| `laranest which-php` | Which PHP the current folder uses |
| `laranest php:list` / `php:install <v>` / `php:update [v]` / `php:remove <v>` | Manage PHP versions |
| `laranest trust [--status]` | Trust the local CA (the Windows user store on Windows and WSL; the system + browsers on Linux) |
| `laranest setup-ports` | Allow ports 80/443 without root (Linux) |
| `laranest shims enable` / `disable` | Plain `php`/`composer` per project |
| `laranest install` / `uninstall` | Install or remove the background service (systemd user unit; on Windows, the sign-in entry) |
| `laranest serve` | Run the daemon in the foreground |

### Updating

The panel tells you when a new Laranest is published (*Update now*, or
*Settings → Check for updates*); from a terminal, `laranest update`. Running
the installer again does the same. Your apps, settings and PHP versions are
kept: on Linux the apps keep running meanwhile, on Windows they restart with
Laranest.

### Uninstall

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
the daemon supervises them itself). See
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
make publish     # publish the current commit as a release (see docs/ARCHITECTURE.md)
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
