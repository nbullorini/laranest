# End-to-end tests

Everything runs against **isolated** Laranest instances (their own
config/state/data and ports, app ids `e2e-*` / `rg-*`), so the developer's real
apps, `.bashrc`, `.wslconfig`, Windows registry and certificate stores are
never touched.

```bash
make go                                   # fresh bin/laranest
test/e2e/services.sh up                   # MySQL 8.4, PostgreSQL 17, Redis 7 (with a password) and Mailpit on unique ports

go test -tags integration ./internal/detect -run Integration -v   # service checks with real credentials
test/e2e/regression.sh                    # every feature (set PW_DIR=… to include the UI)
test/e2e/run-projects.sh [name ...]       # real open source projects (projects.tsv)

test/e2e/services.sh down
```

On Windows (PowerShell 5.1+, from the repo or a `\\wsl.localhost\…` path):

```powershell
make windows                                                  # (in WSL/Linux) bin/laranest.exe
powershell -ExecutionPolicy Bypass -File test\e2e\regression.ps1 -Laranest bin\laranest.exe [-PWDir <dir with playwright-core>]
powershell -ExecutionPolicy Bypass -File test\e2e\run-projects.ps1 -Laranest bin\laranest.exe [-Only laravel,fdemo]
```

`regression.ps1` covers the same features as `regression.sh` plus the Windows
pieces: php-cgi workers (and that they serve requests in parallel), the
supervisor's Job Objects (no orphans when the daemon stops or is killed),
Horizon → `queue:work`, the `ext-pcntl` hint, updating PHP with the apps
stopped first and the UI in Edge. `run-projects.ps1` uses the databases
`e2e_w_<name>` (create them with `docker exec` like `run-projects.sh` does) on
the `services.sh` containers, which WSL forwards to Windows' localhost.
Hyper-V often reserves the range that contains Redis's 63901 on Windows
(`netsh int ipv4 show excludedportrange tcp`): then run a second Redis, e.g.
`docker run -d --name laranest-e2e-redis-w -p 127.0.0.1:26390:6379 redis:7
redis-server --requirepass s3cret`, and pass `-RedisPort 26390`.

## regression.sh

Creates a project with `laranest composer create-project` (no system PHP) and
covers: API and security (Host/Origin), discovery, detection, adding apps and
validation, proxy and HTTPS (CA, redirects, 404/503), systemd processes
(restart after crashes, SSE logs), warnings and `.env` fixes, missing
extensions, Vite (CORS / stale `public/hot`), services with real credentials,
groups, renaming, PHP versions (updating keeps `php.ini` and restarts the
apps), the CLI (`php`, `composer`, `npm`, shims with a temporary HOME), WSL
with a fake Windows profile, port errors, persistence across daemon restarts
and the UI in headless Chrome (`ui.mjs`; `PW_DIR` must point to a directory
with `node_modules/playwright-core`).

## run-projects.sh

For each row of `projects.tsv`: clones the project, prepares `.env` (SQLite,
or its own database on the MySQL/PostgreSQL of `services.sh`), installs with
`laranest composer`, registers the app (HTTPS by default), applies the
one-click `APP_KEY` fix the panel offers, migrates, builds assets with
`laranest npm` (or the row's `cmd:`), starts it and probes it over HTTPS. Results go to
`~/.cache/laranest-e2e/results/`, logs to `~/.cache/laranest-e2e/logs/`.
`edge` rows check that Laranest rejects (and explains) what it cannot run.
