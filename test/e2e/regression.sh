#!/usr/bin/env bash
# Full regression of Laranest's features against an isolated instance.
#
#   test/e2e/services.sh up            # real MySQL/Redis/Mailpit for service checks
#   test/e2e/regression.sh
#
# Isolation: own XDG dirs and ports (9180/8180/8580), app ids "rg-*", a fake
# Windows profile and a temporary HOME for shell rc edits. It never touches
# the developer's real apps, .bashrc, .wslconfig or certificate stores.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
RG="${RG_ROOT:-$HOME/.cache/laranest-regression}"
LN="$REPO/bin/laranest"
rm -rf "$RG" && mkdir -p "$RG"/{xdg/config/laranest,fakewin,home}
# Before overriding XDG_CONFIG_HOME, or gh no longer finds its login.
export GH_TOKEN="${GH_TOKEN:-$(gh auth token 2>/dev/null)}" # PHP builds release may be private
export XDG_CONFIG_HOME="$RG/xdg/config" XDG_STATE_HOME="$RG/xdg/state" XDG_DATA_HOME="$RG/xdg/data"
PANEL=127.0.0.1:9180 HTTP=8180 HTTPS=8580
API="http://$PANEL/api"
CA="$XDG_STATE_HOME/laranest/caddy/pki/authorities/local/root.crt"
printf '[wsl2]\r\nmemory=8GB\r\n' > "$RG/fakewin/.wslconfig"
cat > "$XDG_CONFIG_HOME/laranest/config.json" <<EOF
{"panel_addr":"$PANEL","http_port":$HTTP,"https_port":$HTTPS,"domain":"localhost",
 "wsl_distro":"Ubuntu","windows_profile":"$RG/fakewin"}
EOF

PASS=0 FAIL=0 FAILED=()
ok()   { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗ %s\033[0m %s\n' "$1" "${2:-}"; }
check() { local name=$1; shift; if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi; }
eq()   { [ "$2" = "$3" ] && ok "$1" || bad "$1" "(want '$3', got '$2')"; }
has()  { grep -q -- "$3" <<<"$2" && ok "$1" || bad "$1" "(no '$3' in: ${2:0:200})"; }
hasnt(){ grep -q -- "$3" <<<"$2" && bad "$1" "(unexpected '$3')" || ok "$1"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
j()    { curl -s "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
warns(){ j "$API/apps/$1" | jq -r '[.app.warnings[].code] | join(",")'; }
pstate(){ j "$API/apps/$1" | jq -r --arg p "$2" '.app.procs[] | select(.name==$p) | .state'; }
unitpid(){ systemctl --user show -p MainPID --value "laranest-$1-$2.service"; }
waitfor() { local t=$1; shift; for _ in $(seq "$t"); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }

start_daemon() {
  nohup "$LN" serve > "$RG/daemon.log" 2>&1 &
  DAEMON=$!
  waitfor 20 curl -sf "$API/health"
}
stop_daemon() { kill "$DAEMON" 2>/dev/null; wait "$DAEMON" 2>/dev/null; }

cleanup() {
  for id in $(j "$API/apps" 2>/dev/null | jq -r '.[].id' 2>/dev/null); do j -X DELETE "$API/apps/$id" >/dev/null; done
  stop_daemon
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
section "Test project (created only with Laranest: no system PHP/Composer)"
APP="$RG/apps/demo"
mkdir -p "$RG/apps"
check "laranest composer create-project" "$LN" composer create-project laravel/laravel "$APP" --no-interaction --quiet
cd "$APP"
printf '\nuse Illuminate\\Support\\Facades\\Schedule;\nSchedule::command("inspire")->everyMinute();\n' >> routes/console.php
check "laranest php artisan" "$LN" php artisan --version
check "laranest npm install + build" bash -c "'$LN' npm install --silent --no-audit --no-fund && '$LN' npm run build"
mkdir -p "$RG/apps/notlaravel" && echo '{}' > "$RG/apps/notlaravel/composer.json"
cd "$RG"

# ---------------------------------------------------------------------------
section "Daemon, API and security"
check "the daemon starts" start_daemon
has  "health" "$(j "$API/health")" '"status":"ok"'
eq   "foreign Host → 403" "$(code -H 'Host: evil.com' "$API/health")" 403
eq   "POST cross-origin → 403" "$(code -X POST -H 'Origin: http://evil.localhost' "$API/apps/x/stop")" 403
eq   "SPA route → index" "$(code "http://$PANEL/apps/whatever")" 200
eq   "unknown API → 404" "$(code "$API/nope")" 404
SYS=$(j "$API/system")
eq   "configured ports" "$(jq -r '"\(.http_port)/\(.https_port)"' <<<"$SYS")" "$HTTP/$HTTPS"
eq   "proxy without errors" "$(jq -r '.proxy_error // "none"' <<<"$SYS")" none

section "Discovery and folders"
eq   "invalid folder rejected" "$(code -X PATCH "$API/system" -d '{"scan_dirs":["/no/existe"]}')" 422
eq   "relative path rejected" "$(code -X PATCH "$API/system" -d '{"scan_dirs":["rel"]}')" 422
j -X PATCH "$API/system" -d "{\"scan_dirs\":[\"$RG/apps\"]}" >/dev/null
has  "discover finds the project" "$(j "$API/discover")" "$APP"
hasnt "discover skips non-Laravel folders" "$(j "$API/discover")" notlaravel
has  "folder autocompletion" "$(j "$API/fs?prefix=$RG/apps/de")" "$APP"

section "Detection"
INS=$(j -X POST "$API/inspect" -d "{\"path\":\"$APP\"}")
has  "detects Laravel" "$INS" '"is_laravel":true'
eq   "counts scheduled tasks" "$(jq -r .detection.scheduled_tasks <<<"$INS")" 1
eq   "recommends queue+scheduler" "$(jq -c '.detection.recommended | [.queue,.scheduler]' <<<"$INS")" '[true,true]'
has  "rejects non-Laravel projects" "$(j -X POST "$API/inspect" -d "{\"path\":\"$RG/apps/notlaravel\"}")" 'does not look like a Laravel project'

section "Adding apps and validation"
ADD=$(j -X POST "$API/apps" -d "{\"path\":\"$APP\",\"id\":\"rg-demo\",\"group\":\"Regression\",\"start\":true}")
eq   "add with start" "$(jq -r '.app.state' <<<"$ADD")" running
eq   "HTTPS by default" "$(jq -r '.app.https' <<<"$ADD")" true
eq   "URL https" "$(jq -r '.app.url' <<<"$ADD")" "https://rg-demo.localhost:$HTTPS"
eq   "group saved" "$(jq -r '.app.group' <<<"$ADD")" Regression
eq   "duplicate path → 409" "$(code -X POST "$API/apps" -d "{\"path\":\"$APP\",\"id\":\"rg-otra\"}")" 409
eq   "reserved id → 422" "$(code -X PATCH "$API/apps/rg-demo" -d '{"id":"laranest"}')" 422
eq   "invalid id → 422" "$(code -X PATCH "$API/apps/rg-demo" -d '{"id":"Con Espacios"}')" 422
eq   "group too long → 422" "$(code -X PATCH "$API/apps/rg-demo" -d "{\"group\":\"$(printf 'x%.0s' $(seq 50))\"}")" 422

section "Proxy, HTTPS and certificates"
waitfor 20 test -f "$CA"
check "the local CA exists" test -s "$CA"
waitfor 30 bash -c "[ \$(curl -s --cacert '$CA' -o /dev/null -w '%{http_code}' https://rg-demo.localhost:$HTTPS/) = 200 ]"
eq   "HTTPS verified with the CA" "$(code --cacert "$CA" "https://rg-demo.localhost:$HTTPS/")" 200
eq   "without the CA the certificate is not trusted" "$(curl -s -o /dev/null -w '%{http_code}' "https://rg-demo.localhost:$HTTPS/" ; echo -n " $?" | tr -d '\n')" "000 60"
eq   "http → https (308, keeps the path)" "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://rg-demo.localhost:$HTTP/a/b?c=1")" "308 https://rg-demo.localhost:$HTTPS/a/b?c=1"
eq   "static file" "$(code --cacert "$CA" "https://rg-demo.localhost:$HTTPS/robots.txt")" 200
eq   "unknown domain → 404" "$(code "http://nada.localhost:$HTTP/")" 404
eq   "panel via laranest.localhost" "$(code "http://laranest.localhost:$HTTP/api/health")" 200
has  "Laranest's PHP trusts the CA (bundle)" "$(cd "$APP" && "$LN" php -r "echo strlen(file_get_contents('https://rg-demo.localhost:$HTTPS/'))>0?'trusts':'no';")" trusts
P=$(unitpid rg-demo web)
j -X PATCH "$API/apps/rg-demo" -d '{"https":false}' >/dev/null
eq   "disabling HTTPS → http" "$(code "http://rg-demo.localhost:$HTTP/")" 200
eq   "toggling HTTPS does not restart the app" "$(unitpid rg-demo web)" "$P"
j -X PATCH "$API/apps/rg-demo" -d '{"https":true}' >/dev/null
TR=$(j "$API/trust")
eq   "trust status available" "$(jq -r .ready <<<"$TR")" true

section "Processes (systemd units)"
eq   "web running" "$(pstate rg-demo web)" running
eq   "queue running" "$(pstate rg-demo queue)" running
eq   "scheduler running" "$(pstate rg-demo scheduler)" running
check "FPM uses Laranest's PHP" bash -c "systemctl --user show -p ExecStart --value laranest-rg-demo-web.service | grep -q '$XDG_DATA_HOME/laranest/php/'"
P=$(unitpid rg-demo queue); [ "${P:-0}" -gt 0 ] && kill -9 "$P" # never "kill 0": it would kill this script
waitfor 15 bash -c "[ \"\$(systemctl --user show -p NRestarts --value laranest-rg-demo-queue.service)\" -ge 1 ] && [ \"\$(systemctl --user show -p SubState --value laranest-rg-demo-queue.service)\" = running ]"
eq   "a crashed worker restarts on its own" "$(pstate rg-demo queue)" running
P=$(unitpid rg-demo queue)
j -X POST "$API/apps/rg-demo/restart" -d '{"process":"queue"}' >/dev/null
check "restart a process" test "$(unitpid rg-demo queue)" != "$P"
LOGS=$(timeout 3 curl -sN "$API/apps/rg-demo/logs/scheduler")
has  "live logs (SSE)" "$LOGS" 'data: {'
eq   "logs of an unknown process → error" "$(code "$API/apps/rg-demo/logs/nope")" 422
j -X POST "$API/apps/rg-demo/stop" >/dev/null
eq   "stop → state" "$(j "$API/apps/rg-demo" | jq -r .app.state)" stopped
eq   "stopped app → 503" "$(code --cacert "$CA" "https://rg-demo.localhost:$HTTPS/")" 503
check "stopping leaves no units" bash -c "! systemctl --user is-active -q laranest-rg-demo-web.service"
j -X POST "$API/apps/rg-demo/start" >/dev/null
waitfor 30 bash -c "[ \$(curl -s --cacert '$CA' -o /dev/null -w '%{http_code}' https://rg-demo.localhost:$HTTPS/) = 200 ]"
eq   "start again" "$(code --cacert "$CA" "https://rg-demo.localhost:$HTTPS/")" 200

section "Warnings and .env"
W=$(warns rg-demo)
has  "APP_URL mismatch" "$W" app_url
has  "missing storage:link" "$W" storage_link
FIX=$(j "$API/apps/rg-demo" | jq -c '.app.warnings[] | select(.code=="app_url") | {fix, current}')
has  "fix with the current value (diff)" "$FIX" '"current":{"APP_URL":"http://localhost'
eq   "invalid .env key → 422" "$(code -X POST "$API/apps/rg-demo/env" -d '{"values":{"A=B":"1"}}')" 422
P=$(unitpid rg-demo queue)
j -X POST "$API/apps/rg-demo/env" -d "{\"values\":{\"APP_URL\":\"https://rg-demo.localhost:$HTTPS\"}}" >/dev/null
has  ".env updated" "$(grep ^APP_URL "$APP/.env")" "https://rg-demo.localhost:$HTTPS"
hasnt "the warning goes away" "$(warns rg-demo)" app_url
check "workers restart with the new .env" test "$(unitpid rg-demo queue)" != "$P"
# Extension the build lacks, declared by a (fake) locked package.
cp "$APP/composer.lock" "$RG/composer.lock.bak"
jq '.packages += [{"name":"acme/needs-oci8","version":"1.0.0","require":{"ext-oci8":"*"}}]' "$RG/composer.lock.bak" > "$APP/composer.lock"
has  "missing extension (ext-oci8)" "$(warns rg-demo)" ext_oci8
cp "$RG/composer.lock.bak" "$APP/composer.lock"
# Vite: stale hot file, then a dev server rejecting the app's origin (CORS).
echo "http://127.0.0.1:1" > "$APP/public/hot"
has  "public/hot without a dev server" "$(warns rg-demo)" vite_hot_stale
python3 -c '
import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(s):
        s.send_response(200)
        if s.headers.get("Origin") == "http://localhost": s.send_header("Access-Control-Allow-Origin", "http://localhost")
        s.end_headers()
    def log_message(*a): pass
http.server.HTTPServer(("127.0.0.1", 18999), H).serve_forever()' & VITE=$!
sleep 0.5; echo "http://127.0.0.1:18999" > "$APP/public/hot"
has  "Vite rejects the origin (CORS)" "$(warns rg-demo)" vite_cors
kill $VITE; rm -f "$APP/public/hot"
# Services with real credentials (needs services.sh up).
if docker ps --format '{{.Names}}' | grep -q laranest-e2e-mysql; then
  cp "$APP/.env" "$RG/env.bak"
  python3 - "$APP/.env" <<'PY'
import sys
p = sys.argv[1]; lines = [l for l in open(p).read().splitlines() if not l.startswith(("DB_", "REDIS_", "CACHE_STORE", "MAIL_"))]
lines += ["DB_CONNECTION=mysql", "DB_HOST=127.0.0.1", "DB_PORT=33901", "DB_DATABASE=e2e", "DB_USERNAME=app", "DB_PASSWORD=wrong",
          "CACHE_STORE=redis", "REDIS_HOST=127.0.0.1", "REDIS_PORT=63901", "REDIS_PASSWORD=bad", "MAIL_MAILER=smtp", "MAIL_HOST=127.0.0.1", "MAIL_PORT=63901"]
open(p, "w").write("\n".join(lines) + "\n")
PY
  j -X POST "$API/apps/rg-demo/detect" >/dev/null
  W=$(warns rg-demo)
  has "MySQL with a wrong password" "$W" db_auth
  has "Redis with a wrong password" "$W" redis_auth
  has "SMTP port that is not SMTP" "$W" mail_protocol
  cp "$RG/env.bak" "$APP/.env"; j -X POST "$API/apps/rg-demo/detect" >/dev/null
  hasnt "restoring the .env clears them" "$(warns rg-demo)" db_auth
else
  echo "  (Docker services are down: skipping the checks with real credentials)"
fi

section "Commands"
cmdstatus(){ j "$API/apps/$1/commands/$2" | jq -r .status; }
CID=$(j -X POST "$API/apps/rg-demo/commands" -d '{"line":"php artisan --version"}' | jq -r .id)
waitfor 60 bash -c "[ \"\$(curl -s $API/apps/rg-demo/commands/$CID | jq -r .status)\" != running ]"
eq   "run a command in the project" "$(cmdstatus rg-demo "$CID")" ok
has  "its output is kept" "$(j "$API/apps/rg-demo/commands/$CID" | jq -r .output)" "Laravel Framework"
eq   "the laranest prefix is optional" "$(j -X POST "$API/apps/rg-demo/commands" -d '{"line":"laranest php -v"}' | jq -r '.args[0]')" php
eq   "only project tooling (no uninstall) -> 422" "$(code -X POST "$API/apps/rg-demo/commands" -d '{"line":"uninstall"}')" 422
eq   "only project tooling (no rm) -> 422" "$(code -X POST "$API/apps/rg-demo/commands" -d '{"line":"rm -rf /"}')" 422
CID=$(j -X POST "$API/apps/rg-demo/commands" -d '{"line":"php -r \"exit(3);\""}' | jq -r .id)
waitfor 60 bash -c "[ \"\$(curl -s $API/apps/rg-demo/commands/$CID | jq -r .status)\" != running ]"
eq   "a failing command reports its exit code" "$(j "$API/apps/rg-demo/commands/$CID" | jq -r '"\(.status) \(.exit_code)"')" "failed 3"
CID=$(j -X POST "$API/apps/rg-demo/commands" -d '{"line":"php -r \"sleep(120);\""}' | jq -r .id)
sleep 2
j -X POST "$API/apps/rg-demo/commands/$CID/stop" >/dev/null
check "stop a running command" waitfor 15 bash -c "[ \"\$(curl -s $API/apps/rg-demo/commands/$CID | jq -r .status)\" = stopped ]"
check "...and its processes" bash -c "! pgrep -f 'sleep\(120\)' >/dev/null"
FIXCMD=$(j "$API/apps/rg-demo" | jq -r '.app.warnings[] | select(.code=="storage_link") | .command')
eq   "warnings offer the command that fixes them" "$FIXCMD" "php artisan storage:link"
CID=$(j -X POST "$API/apps/rg-demo/commands" -d "{\"line\":\"$FIXCMD\"}" | jq -r .id)
waitfor 60 bash -c "[ \"\$(curl -s $API/apps/rg-demo/commands/$CID | jq -r .status)\" != running ]"
hasnt "running it clears the warning" "$(warns rg-demo)" storage_link
eq   "history, newest first" "$(j "$API/apps/rg-demo/commands" | jq -r '.[0].line')" "php artisan storage:link"

section "App settings: group, rename, PHP"
P=$(unitpid rg-demo web)
j -X PATCH "$API/apps/rg-demo" -d '{"group":"  Other   group "}' >/dev/null
eq   "group normalized" "$(j "$API/apps/rg-demo" | jq -r .app.group)" "Other group"
eq   "changing the group does not restart" "$(unitpid rg-demo web)" "$P"
j -X PATCH "$API/apps/rg-demo" -d '{"processes":{"queue":true,"horizon":false,"scheduler":false,"reverb":false,"vite":false}}' >/dev/null
eq   "disabling the scheduler stops it" "$(systemctl --user is-active laranest-rg-demo-scheduler.service)" inactive
j -X PATCH "$API/apps/rg-demo" -d '{"id":"rg-renamed"}' >/dev/null
waitfor 30 bash -c "[ \$(curl -s --cacert '$CA' -o /dev/null -w '%{http_code}' https://rg-renamed.localhost:$HTTPS/) = 200 ]"
eq   "renaming serves the new subdomain" "$(code --cacert "$CA" "https://rg-renamed.localhost:$HTTPS/")" 200
check "the old subdomain is no longer served" test "$(code --cacert "$CA" "https://rg-demo.localhost:$HTTPS/")" != 200
check "renaming keeps the command history" test "$(j "$API/apps/rg-renamed/commands" | jq length)" -ge 5
eq   "unknown PHP → 422" "$(code -X PATCH "$API/apps/rg-renamed" -d '{"php":"7.0"}')" 422
eq   "…and the app keeps running" "$(j "$API/apps/rg-renamed" | jq -r .app.state)" running

section "PHP versions"
PHPI=$(j "$API/php")
check "lists installed versions" test "$(jq '.installed | length' <<<"$PHPI")" -ge 1
check "lists downloadable versions" test "$(jq '.available | length' <<<"$PHPI")" -ge 1
eq   "does not use the system PHP" "$(jq -r '[.installed[].source] | unique | join(",")' <<<"$PHPI")" laranest
INUSE=$(jq -r '.installed[] | select(.used_by | length > 0) | .version' <<<"$PHPI" | head -1)
eq   "refuses to remove a version in use" "$(code -X DELETE "$API/php/$INUSE")" 422
echo "8.0.0" > "$XDG_DATA_HOME/laranest/php/$INUSE/VERSION"
has  "detects an available update" "$(j "$API/php?refresh=1" | jq -c ".installed[] | select(.version==\"$INUSE\") | .update")" true
echo "; regression marker" >> "$XDG_DATA_HOME/laranest/php/$INUSE/php.ini"
P=$(unitpid rg-renamed web)
UPD=$(j -X POST "$API/php/$INUSE/update")
has  "updating restarts the apps using it" "$UPD" rg-renamed
check "keeps the php.ini" grep -q "regression marker" "$XDG_DATA_HOME/laranest/php/$INUSE/php.ini"
check "the app keeps responding" waitfor 30 bash -c "[ \$(curl -s --cacert '$CA' -o /dev/null -w '%{http_code}' https://rg-renamed.localhost:$HTTPS/) = 200 ]"
OTHER=$(jq -r ".available[0].minor // empty" <<<"$(j "$API/php")")
if [ -n "$OTHER" ]; then
  eq   "download another version ($OTHER)" "$(code -X POST "$API/php/$OTHER")" 200
  eq   "remove an unused version" "$(code -X DELETE "$API/php/$OTHER")" 200
fi

section "CLI"
cd "$APP"
has  "laranest version" "$("$LN" version)" .
has  "laranest status" "$("$LN" status)" "panel:"
has  "which-php uses the app's PHP" "$("$LN" which-php)" laranest
has  "laranest php -v" "$("$LN" php -v)" "PHP 8."
has  "laranest composer" "$("$LN" composer --version 2>&1)" "Composer version"
has  "laranest npm/node" "$("$LN" node -v)" "v"
APPPHP=$("$LN" php -r 'echo PHP_VERSION;')
eq   "from Node, php is the project's PHP (shims)" "$("$LN" node -e 'process.stdout.write(require("child_process").execSync("php -r \"echo PHP_VERSION;\"").toString())')" "$APPPHP"
has  "php:list" "$("$LN" php:list)" "laranest"
has  "trust --status" "$("$LN" trust --status 2>&1)" "Linux"
has  "setup-ports --help" "$("$LN" setup-ports --help)" "port 80"
HOME="$RG/home" "$LN" shims enable >/dev/null
check "shims: creates php and composer" test -L "$XDG_DATA_HOME/laranest/shims/php"
has  "shims: block in .bashrc" "$(cat "$RG/home/.bashrc")" "laranest shims"
has  "the php shim picks the app's version" "$("$XDG_DATA_HOME/laranest/shims/php" -r 'echo PHP_VERSION;')" "8."
HOME="$RG/home" "$LN" shims disable >/dev/null
hasnt "shims disable cleans .bashrc" "$(cat "$RG/home/.bashrc")" "laranest"
cd "$RG"

section "WSL (fake Windows profile)"
WS=$(j "$API/wsl")
eq   "detects WSL and the profile" "$(jq -r '"\(.is_wsl)/\(.supported)"' <<<"$WS")" true/true
j -X PATCH "$API/wsl" -d '{"keep_alive":true}' >/dev/null
check "keep-alive writes instanceIdleTimeout=-1" grep -q "instanceIdleTimeout=-1" "$RG/fakewin/.wslconfig"
check "keep-alive keeps CRLF" grep -q $'memory=8GB\r' "$RG/fakewin/.wslconfig"
j -X PATCH "$API/wsl" -d '{"autostart":true}' >/dev/null
check "autostart creates the startup script" test -f "$RG/fakewin/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/Laranest.vbs"
j -X PATCH "$API/wsl" -d '{"keep_alive":false}' >/dev/null
eq   "reverting restores the file" "$(cat "$RG/fakewin/.wslconfig")" "$(printf '[wsl2]\r\nmemory=8GB\r')"
check "reverting removes the autostart" test ! -f "$RG/fakewin/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/Laranest.vbs"

section "Ports and proxy errors"
python3 -c 'import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("127.0.0.1",8181)); s.listen(); time.sleep(8)' & BLOCK=$!
sleep 0.5
SYS=$(j -X PATCH "$API/system" -d '{"http_port":8181}')
has  "busy port → error with a hint" "$(jq -r '"\(.proxy_error) \(.proxy_hint)"' <<<"$SYS")" "address already in use"
kill $BLOCK
eq   "same HTTP and HTTPS port → 422" "$(code -X PATCH "$API/system" -d "{\"http_port\":$HTTPS}")" 422
j -X PATCH "$API/system" -d "{\"http_port\":$HTTP}" >/dev/null
eq   "back to the original port" "$(code --cacert "$CA" "https://rg-renamed.localhost:$HTTPS/")" 200

section "Persistence: the daemon restarts, the apps keep running"
P=$(unitpid rg-renamed web)
stop_daemon
check "with the daemon down the app stays alive" kill -0 "$P"
start_daemon
eq   "same process after restarting the daemon" "$(unitpid rg-renamed web)" "$P"
check "and keeps responding" waitfor 20 bash -c "[ \$(curl -s --cacert '$CA' -o /dev/null -w '%{http_code}' https://rg-renamed.localhost:$HTTPS/) = 200 ]"
eq   "config persisted (group)" "$(j "$API/apps/rg-renamed" | jq -r .app.group)" "Other group"
check "command history persisted" test "$(j "$API/apps/rg-renamed/commands" | jq length)" -ge 5

section "UI (Chrome headless)"
if [ -n "${PW_DIR:-}" ]; then
  if node "$HERE/ui.mjs" "http://$PANEL" > "$RG/ui.log" 2>&1; then ok "UI walkthrough without errors"; else bad "UI walkthrough" "$(tail -5 "$RG/ui.log")"; fi
  sed 's/^/    /' "$RG/ui.log"
else
  echo "  (PW_DIR not set: skipping the UI; see ui.mjs)"
fi

section "Removing the app"
j -X DELETE "$API/apps/rg-renamed" >/dev/null
eq   "it is gone" "$(j "$API/apps" | jq length)" 0
check "its units were stopped" bash -c "! systemctl --user is-active -q laranest-rg-renamed-web.service"
check "the project was not touched" test -f "$APP/artisan"

printf '\n\033[1mResult: %d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
for f in "${FAILED[@]}"; do echo "  - $f"; done
[ "$FAIL" -eq 0 ]
