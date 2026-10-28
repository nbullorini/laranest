#!/usr/bin/env bash
# End-to-end test of Laranest on macOS, for a CI runner (it installs the real
# launchd agent with the default folders and ports, so don't run it on your Mac
# unless you mean to):
#
#   make && CI=true test/e2e/macos.sh
#
# Covers what is specific to macOS: the launchd agent, PHP from static-php's
# macOS builds, PHP-FPM under Laranest's own supervisor, HTTPS, custom
# processes, the web on a port, stopping whole process groups, cleaning up
# after a daemon that was killed, and uninstalling.
set -uo pipefail
[ "${CI:-}" = true ] || { echo "this installs Laranest for real: run it on a CI runner (CI=true)"; exit 2; }

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
LN="$REPO/bin/laranest"
ROOT="${RUNNER_TEMP:-$(mktemp -d)}/laranest-macos"
APP="$ROOT/app"
API=http://127.0.0.1:9150/api
STATE="$HOME/.local/state/laranest"
rm -rf "$ROOT" && mkdir -p "$APP/public"

PASS=0 FAIL=0 FAILED=()
ok()   { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗ %s\033[0m %s\n' "$1" "${2:-}"; }
check() { local name=$1; shift; if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi; }
eq()   { [ "$2" = "$3" ] && ok "$1" || bad "$1" "(want '$3', got '$2')"; }
has()  { grep -q -- "$3" <<<"$2" && ok "$1" || bad "$1" "(no '$3' in: ${2:0:300})"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
j()    { curl -s "$@"; }
waitfor() { local t=$1; shift; for _ in $(seq "$t"); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }
appstate() { j "$API/apps/mac" | jq -r .app.state; }
pstate() { j "$API/apps/mac" | jq -r --arg p "$1" '.app.procs[] | select(.name==$p) | .state'; }
body() { curl -s --cacert "$STATE/caddy/pki/authorities/local/root.crt" "$@"; }
served() { body "https://mac.localhost$SFX/" | grep -q -- "$1"; }
sleepers() { pgrep -f "^sleep 400[01]"; }

dump() {
  echo "--- daemon log"; tail -n 80 "$STATE/laranest.log" 2>/dev/null
  echo "--- processes"; j "$API/apps/mac" | jq -c '.app.procs[]?' 2>/dev/null
  for p in web worker; do echo "--- $p logs"; curl -s -m 2 "$API/apps/mac/logs/$p" | tail -n 20; done
  echo "--- ps"; ps -axo pid,ppid,pgid,stat,command | grep -E "php|sleep|laranest" | grep -v grep
  echo "--- state files"; find "$STATE" -maxdepth 3 \( -name "*.sock" -o -name "*.conf" -o -name "*.json" \) -print 2>/dev/null
  for f in "$STATE"/apps/mac/fpm.conf; do [ -f "$f" ] && { echo "--- $f"; cat "$f"; }; done
}

# ---------------------------------------------------------------------------
section "Install (launchd agent)"
"$LN" install || { bad "laranest install"; dump; exit 1; }
check "LaunchAgent written" test -f "$HOME/Library/LaunchAgents/dev.laranest.plist"
check "launchd runs it" launchctl print "gui/$(id -u)/dev.laranest"
has "laranest status" "$("$LN" status 2>&1)" "active"
PORTS=$(j "$API/system" | jq -r '"\(.http_port) \(.https_port)"')
read -r HTTP HTTPS <<<"$PORTS"
echo "  (proxy on $HTTP/$HTTPS)"
SFX=""; [ "$HTTPS" = 443 ] || SFX=":$HTTPS"
HSFX=""; [ "$HTTP" = 80 ] || HSFX=":$HTTP"

# ---------------------------------------------------------------------------
section "An app with PHP downloaded for macOS"
echo '{"require":{"php":"^8.4","laravel/framework":"^12.0"}}' > "$APP/composer.json"
touch "$APP/artisan"
mkdir -p "$APP"/storage/{logs,framework/{cache,sessions,views}} "$APP/bootstrap/cache"
echo 'APP_KEY=base64:macos' > "$APP/.env"
echo '<?php echo "ok ", PHP_VERSION, " ", PHP_OS_FAMILY, " ", PHP_SAPI;' > "$APP/public/index.php"
add=$(j -X POST "$API/apps" -d "{\"path\":\"$APP\",\"id\":\"mac\",\"php\":\"8.4\",\"start\":true,
  \"processes\":{\"queue\":false,\"horizon\":false,\"scheduler\":false,\"reverb\":false,\"vite\":false}}")
has "app added" "$add" '"id":"mac"'
waitfor 240 served "ok 8.4." && ok "served over HTTPS by PHP-FPM" || { bad "served over HTTPS by PHP-FPM"; dump; }
has "PHP is the macOS build, through FPM" "$(body "https://mac.localhost$SFX/")" "Darwin fpm-fcgi"
eq "http redirects to https" "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://mac.localhost$HSFX/x")" "308 https://mac.localhost$SFX/x"
has "laranest php works in the project" "$(cd "$APP" && "$LN" php -r 'echo PHP_OS_FAMILY;' 2>&1)" "Darwin"
# The System Keychain needs a password prompt: on a CI runner only check it doesn't hang.
perl -e 'alarm shift; exec @ARGV' 60 "$LN" trust < /dev/null > "$ROOT/trust.log" 2>&1; echo "  (laranest trust exited $?)"

# ---------------------------------------------------------------------------
section "Custom process (its whole process group is supervised)"
j -X PATCH "$API/apps/mac" -d '{"custom":[{"name":"worker","command":"bash -c \"sleep 4000 & sleep 4001\""}]}' > /dev/null
waitfor 30 test "$(pstate worker)" = running && ok "custom process running" || { bad "custom process running"; dump; }
waitfor 10 test "$(sleepers | wc -l | tr -d ' ')" = 2 && ok "with its child" || { bad "with its child"; dump; }
web_pid=$(j "$API/apps/mac" | jq -r '.app.procs[] | select(.name=="web") | .pid')

section "Stop leaves no processes behind"
j -X POST "$API/apps/mac/stop" > /dev/null
waitfor 20 test "$(appstate)" = stopped && ok "app stopped" || bad "app stopped"
waitfor 15 bash -c '! pgrep -f "^sleep 400[01]"' && ok "child of the custom process gone" || bad "child of the custom process gone" "$(sleepers)"
check "PHP-FPM gone" bash -c "! kill -0 $web_pid"

# ---------------------------------------------------------------------------
section "A killed daemon: launchd restarts it and it cleans up"
j -X POST "$API/apps/mac/start" > /dev/null
waitfor 60 served "ok 8.4." && ok "started again" || bad "started again"
waitfor 10 test "$(sleepers | wc -l | tr -d ' ')" = 2
old=$(sleepers | tr '\n' ' ')
pkill -9 -f "$LN serve" || pkill -9 -x laranest
sleep 2
waitfor 60 curl -sf "$API/health" && ok "launchd restarted the daemon" || { bad "launchd restarted the daemon"; dump; }
waitfor 60 served "ok 8.4." && ok "the app came back" || { bad "the app came back"; dump; }
gone=1; for p in $old; do kill -0 "$p" 2>/dev/null && gone=0; done
eq "the old processes were reaped" "$gone" 1

# ---------------------------------------------------------------------------
section "Web served by a command on a port"
j -X PATCH "$API/apps/mac" -d '{"web_mode":"port","commands":{"web":"php -S 127.0.0.1:{port} -t public"}}' > /dev/null
waitfor 60 served cli-server && ok "php -S behind the proxy" || { bad "php -S behind the proxy"; dump; }
j -X PATCH "$API/apps/mac" -d '{"web_mode":"","commands":{}}' > /dev/null
waitfor 60 served fpm-fcgi && ok "back to PHP-FPM" || bad "back to PHP-FPM"

# ---------------------------------------------------------------------------
section "Uninstall"
"$LN" uninstall
check "agent removed" test ! -f "$HOME/Library/LaunchAgents/dev.laranest.plist"
check "daemon stopped" bash -c '! curl -sf -m 2 '"$API"'/health'
waitfor 15 bash -c '! pgrep -f "^sleep 400[01]"' && ok "apps stopped with it" || bad "apps stopped with it" "$(sleepers)"
check "no PHP-FPM left" bash -c '! pgrep -f php-fpm'

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || { printf '  - %s\n' "${FAILED[@]}"; exit 1; }
