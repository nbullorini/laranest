#!/usr/bin/env bash
# Installs real open-source Laravel projects with Laranest's own tooling
# (laranest composer/php/npm: no system PHP needed), registers them in an
# isolated Laranest instance, starts them and probes them over HTTPS.
#
#   test/e2e/services.sh up
#   test/e2e/run-projects.sh [name ...]       # default: every row of projects.tsv
#
# Results: $E2E_ROOT/results/<name>.json, logs: $E2E_ROOT/logs/<name>.log
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
E2E_ROOT="${E2E_ROOT:-$HOME/.cache/laranest-e2e}"
JOBS="${JOBS:-4}"
LN="$REPO/bin/laranest"

# Isolated instance: its own config/state/data and ports, app ids prefixed
# "e2e-" (systemd unit names are per user, shared with any real Laranest).
# Before overriding XDG_CONFIG_HOME, or gh no longer finds its login.
export GH_TOKEN="${GH_TOKEN:-$(gh auth token 2>/dev/null)}" # PHP builds release may be private
export XDG_CONFIG_HOME="$E2E_ROOT/xdg/config" XDG_STATE_HOME="$E2E_ROOT/xdg/state" XDG_DATA_HOME="$E2E_ROOT/xdg/data"
export E2E_PANEL=127.0.0.1:9170 E2E_HTTP=8170 E2E_HTTPS=8570
API="http://$E2E_PANEL/api"
ROOT_CA="$XDG_STATE_HOME/laranest/caddy/pki/authorities/local/root.crt"
mkdir -p "$E2E_ROOT"/{projects,logs,results} "$XDG_CONFIG_HOME/laranest"

start_daemon() {
  curl -sf "$API/health" >/dev/null && return
  [ -f "$XDG_CONFIG_HOME/laranest/config.json" ] ||
    printf '{"panel_addr":"%s","http_port":%s,"https_port":%s,"domain":"localhost"}\n' "$E2E_PANEL" "$E2E_HTTP" "$E2E_HTTPS" \
      > "$XDG_CONFIG_HOME/laranest/config.json"
  nohup "$LN" serve > "$E2E_ROOT/daemon.log" 2>&1 &
  for _ in $(seq 50); do curl -sf "$API/health" >/dev/null && return; sleep 0.2; done
  echo "daemon did not start"; exit 1
}

# setenv FILE KEY=VALUE... (adds or replaces, keeps the rest)
setenv() {
  python3 - "$@" <<'PY'
import re, sys
path, pairs = sys.argv[1], sys.argv[2:]
try:
    lines = open(path).read().splitlines()
except FileNotFoundError:
    lines = []
for pair in pairs:
    k, v = pair.split("=", 1)
    rx = re.compile(r"^\s*#?\s*" + re.escape(k) + r"\s*=")
    line = f'{k}="{v}"' if (" " in v or "#" in v) else f"{k}={v}"
    for i, l in enumerate(lines):
        if rx.match(l):
            lines[i] = line
            break
    else:
        lines.append(line)
open(path, "w").write("\n".join(lines) + "\n")
PY
}

step() { # step NAME TIMEOUT CMD... → logs, records failures
  local name=$1 t=$2; shift 2
  echo "### $name: $*" >> "$LOG"
  if timeout "$t" "$@" >> "$LOG" 2>&1; then STEPS+=("$name:ok"); return 0; fi
  STEPS+=("$name:fail"); return 1
}

run_project() {
  local name=$1 repo=$2 ref=$3 db=$4 assets=$5 kind=$6
  local id="e2e-$name" dir="$E2E_ROOT/projects/$name"
  LOG="$E2E_ROOT/logs/$name.log"; : > "$LOG"; STEPS=()
  local url="https://$id.localhost:$E2E_HTTPS"

  [ -d "$dir/.git" ] || step clone 600 git clone -q --depth 1 -b "$ref" "https://github.com/$repo" "$dir"
  cd "$dir" || return

  # Detection only: edge cases must be told apart before anything is installed.
  local inspect; inspect=$(curl -s -X POST "$API/inspect" -d "{\"path\":\"$dir\"}")
  if [ "$kind" = edge ]; then
    # What does the user get when trying anyway?
    local add; add=$(curl -s -X POST "$API/apps" -d "{\"path\":\"$dir\",\"id\":\"$id\",\"start\":true}")
    curl -s -X DELETE "$API/apps/$id" >/dev/null
    jq -n --arg n "$name" --argjson i "$inspect" --argjson a "$add" \
      '{name:$n, kind:"edge", inspect:$i, add_error:($a.error // null), add_state:($a.app.state // null)}' > "$E2E_ROOT/results/$name.json"
    return
  fi

  [ -f .env ] || cp .env.example .env 2>/dev/null || touch .env
  setenv .env APP_ENV=local APP_DEBUG=true "APP_URL=$url" \
    REDIS_HOST=127.0.0.1 REDIS_PORT=63901 REDIS_PASSWORD=s3cret \
    MAIL_MAILER=smtp MAIL_HOST=127.0.0.1 MAIL_PORT=10925
  case "$db" in
    sqlite) mkdir -p database && touch database/database.sqlite
            setenv .env DB_CONNECTION=sqlite "DB_DATABASE=$dir/database/database.sqlite" ;;
    mysql)  docker exec laranest-e2e-mysql mysql -uroot -psecret -e "CREATE DATABASE IF NOT EXISTS \`e2e_$name\`" 2>/dev/null
            setenv .env DB_CONNECTION=mysql DB_HOST=127.0.0.1 DB_PORT=33901 "DB_DATABASE=e2e_$name" DB_USERNAME=root DB_PASSWORD=secret ;;
    pgsql)  docker exec laranest-e2e-pg createdb -U app "e2e_$name" 2>/dev/null
            setenv .env DB_CONNECTION=pgsql DB_HOST=127.0.0.1 DB_PORT=54901 "DB_DATABASE=e2e_$name" DB_USERNAME=app DB_PASSWORD=secret ;;
  esac

  step composer 1500 "$LN" composer install --no-interaction --prefer-dist --no-progress

  # Like a user: add the app (not started yet) and apply the one-click fix
  # Laranest offers for the app key before running anything that needs it.
  curl -s -X DELETE "$API/apps/$id" >/dev/null
  curl -s -X POST "$API/apps" -d "{\"path\":\"$dir\",\"id\":\"$id\",\"start\":false}" >/dev/null
  local fix; fix=$(curl -s "$API/apps/$id" | jq -c '[.app.warnings[] | select(.code=="app_key" and .fix) | .fix] | add // empty')
  if [ -n "$fix" ]; then
    STEPS+=("applied-fix:app_key")
    curl -s -X POST "$API/apps/$id/env" -d "{\"values\":$fix}" >/dev/null
  fi
  step migrate 600 "$LN" php artisan migrate --force
  case "$assets" in
    cmd:*) step assets 1200 bash -c "${assets#cmd:}" ;;
    no) ;;
    *) if [ -f package.json ] && jq -e '.scripts.build' package.json >/dev/null 2>&1; then
         if [ -f package-lock.json ]; then step npm 900 "$LN" npm ci --no-audit --no-fund
         else step npm 900 "$LN" npm install --no-audit --no-fund; fi && step build 900 "$LN" npm run build
       fi ;;
  esac

  # Start it (recommended processes, HTTPS by default).
  local add; add=$(curl -s -X POST "$API/apps/$id/start")
  local code=000 final=""
  for _ in $(seq 40); do
    read -r code final < <(curl -s -L --max-redirs 5 --cacert "$ROOT_CA" -o /dev/null -w '%{http_code} %{url_effective}' "$url/")
    case "$code" in 2??) break ;; esac
    sleep 1
  done
  sleep 8 # let workers crash-loop if they're going to
  local view; view=$(curl -s "$API/apps/$id")
  local title; title=$(curl -s -L --max-redirs 5 --cacert "$ROOT_CA" "$url/" | grep -o '<title>[^<]*' | head -1 | sed 's/<title>//')
  jq -n --arg n "$name" --arg kind "$kind" --arg code "$code" --arg final "$final" --arg title "$title" \
    --argjson steps "$(printf '%s\n' "${STEPS[@]}" | jq -R . | jq -s .)" \
    --argjson add "$add" --argjson view "$view" \
    '{name:$n, kind:$kind, http:$code, final:$final, title:$title, steps:$steps,
      add_error:($add.error // null),
      php:$view.app.php_version, laravel:$view.app.detection.laravel_version,
      packages:($view.app.detection.packages|keys), recommended:$view.app.detection.recommended,
      procs:[$view.app.procs[] | {name, state, restarts}],
      warnings:[$view.app.warnings[] | {code, level, message}]}' > "$E2E_ROOT/results/$name.json"
  curl -s -X POST "$API/apps/$id/stop" >/dev/null
}

export -f run_project step setenv
export E2E_ROOT LN API ROOT_CA

start_daemon
rows=$(grep -v '^#' "$HERE/projects.tsv" | grep -v '^\s*$')
if [ $# -gt 0 ]; then rows=$(printf '%s\n' "$rows" | grep -E "^($(IFS='|'; echo "$*"))	"); fi
printf '%s\n' "$rows" | xargs -P "$JOBS" -I{} bash -c 'IFS=$'"'"'\t'"'"' read -r n r ref db a k _ <<<"{}"; run_project "$n" "$r" "$ref" "$db" "$a" "$k"; echo "done: $n"'
