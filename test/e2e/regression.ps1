# Full regression of Laranest on Windows against an isolated instance.
#
#   powershell -ExecutionPolicy Bypass -File test\e2e\regression.ps1 -Laranest bin\laranest.exe
#
# Isolation: its own LARANEST_HOME and ports (9180/8180/8580) and app ids
# "rg-*". It never touches the registry (sign-in entry, PATH), the Windows
# certificate store or other Laranest instances. -PWDir: a folder with
# node_modules\playwright-core to also walk through the UI in Edge.
# Windows PowerShell 5.1 compatible (no &&, no ternaries).
param(
  [string]$Laranest = "$PSScriptRoot\..\..\bin\laranest.exe",
  [string]$Root = "$env:TEMP\laranest-regression",
  [string]$PWDir = "",
  # Hyper-V often reserves 63901 on Windows: see run-projects.ps1.
  [int]$RedisPort = 63901
)
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'
Add-Type -AssemblyName System.Net.Http

# Copy the binary: tests may run it from a WSL path, and the daemon finds
# php-cgi helpers next to itself.
$Bin = "$Root\bin"
if (Test-Path "$Root\home") {
  try { Invoke-RestMethod -Method Post "http://127.0.0.1:9180/api/shutdown" -TimeoutSec 5 | Out-Null; Start-Sleep 3 } catch {}
  Remove-Item -Recurse -Force "$Root\home" -ErrorAction SilentlyContinue
}
New-Item -ItemType Directory -Force "$Bin", "$Root\home\config", "$Root\apps" | Out-Null
Copy-Item -Force $Laranest "$Bin\laranest.exe"
$LN = "$Bin\laranest.exe"
$env:LARANEST_HOME = "$Root\home"
$PANEL = '127.0.0.1:9180'; $HTTP = 8180; $HTTPS = 8580
$API = "http://$PANEL/api"
$Data = "$Root\home\data"
$CA = "$Root\home\state\caddy\pki\authorities\local\root.crt"
Set-Content -Encoding ascii "$Root\home\config\config.json" "{`"panel_addr`":`"$PANEL`",`"http_port`":$HTTP,`"https_port`":$HTTPS,`"domain`":`"localhost`"}"

$script:Pass = 0; $script:Fail = 0; $script:Failed = @()
function Ok($name) { $script:Pass++; Write-Host "  + $name" -ForegroundColor Green }
function Bad($name, $why = '') { $script:Fail++; $script:Failed += $name; Write-Host "  x $name $why" -ForegroundColor Red }
function Check($name, [scriptblock]$cond) { $r = $false; try { $r = & $cond } catch {}; if ($r) { Ok $name } else { Bad $name } }
function Eq($name, $got, $want) { if ("$got" -eq "$want") { Ok $name } else { Bad $name "(want '$want', got '$got')" } }
function Has($name, $text, $needle) { if ("$text".Contains($needle)) { Ok $name } else { $t = "$text"; Bad $name "(no '$needle' in: $($t.Substring(0, [Math]::Min(200, $t.Length))))" } }
function Hasnt($name, $text, $needle) { if ("$text".Contains($needle)) { Bad $name "(unexpected '$needle')" } else { Ok $name } }
function Section($s) { Write-Host "`n$s" -ForegroundColor White }
function WaitFor($secs, [scriptblock]$cond) { for ($i = 0; $i -lt $secs; $i++) { try { if (& $cond) { return $true } } catch {}; Start-Sleep 1 }; return $false }

$client = New-Object System.Net.Http.HttpClient
$client.Timeout = [TimeSpan]::FromSeconds(60)
# Req calls the API without PowerShell's quoting problems with native curl.
function Req($method, $path, $body = $null, $headers = @{}) {
  $msg = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::new($method)), "$API$path"
  foreach ($k in $headers.Keys) { $msg.Headers.TryAddWithoutValidation($k, $headers[$k]) | Out-Null }
  if ($null -ne $body) { $msg.Content = New-Object System.Net.Http.StringContent ($body | ConvertTo-Json -Depth 6 -Compress), ([Text.Encoding]::UTF8), 'application/json' }
  $resp = $client.SendAsync($msg).Result
  $text = $resp.Content.ReadAsStringAsync().Result
  $json = $null; try { $json = $text | ConvertFrom-Json } catch {}
  [pscustomobject]@{ Code = [int]$resp.StatusCode; Text = $text; Json = $json }
}
function App($id) { (Req GET "/apps/$id").Json.app }
function Warns($id) { ((App $id).warnings | ForEach-Object { $_.code }) -join ',' }
function Proc($id, $name) { (App $id).procs | Where-Object { $_.name -eq $name } }
# Code returns the HTTP status of a URL (TLS checked by Laranest's CA unless -k).
function Code($url, [switch]$Insecure, [string]$HostHeader = '') {
  $a = @('-s', '-o', 'NUL', '-w', '%{http_code}', '--ssl-no-revoke', '--max-time', '30')
  if ($Insecure) { $a += '-k' } elseif (Test-Path $CA) { $a += @('--cacert', $CA) }
  if ($HostHeader) { $a += @('-H', "Host: $HostHeader") }
  & curl.exe @a $url
}
function PhpCgi() { @(Get-CimInstance Win32_Process -Filter "Name = 'php-cgi.exe'" | Where-Object { $_.ExecutablePath -like "$Data\*" }) }
function Run($exe, $argsList, $dir) { Push-Location $dir; try { (& $exe @argsList 2>&1 | Out-String) } finally { Pop-Location } }

function Start-Daemon {
  $script:Daemon = Start-Process -FilePath $LN -ArgumentList 'serve' -WindowStyle Hidden -PassThru `
    -RedirectStandardOutput "$Root\daemon.log" -RedirectStandardError "$Root\daemon.err"
  WaitFor 20 { (Req GET '/health').Code -eq 200 }
}
function Stop-Daemon { try { Req POST '/shutdown' | Out-Null } catch {}; if ($script:Daemon) { $script:Daemon.WaitForExit(20000) | Out-Null } }

# ---------------------------------------------------------------------------
Section 'Test project (created only with Laranest: no system PHP/Composer)'
$APP = "$Root\apps\demo"
if (-not (Test-Path "$APP\vendor\autoload.php")) {
  Remove-Item -Recurse -Force $APP -ErrorAction SilentlyContinue # a half-deleted leftover
  Run $LN @('composer', 'create-project', 'laravel/laravel', $APP, '--no-interaction', '--quiet') "$Root\apps" | Out-Null
}
Check 'laranest composer create-project' { Test-Path "$APP\vendor\autoload.php" }
$console = Get-Content -Raw "$APP\routes\console.php"
if (-not $console.Contains('Schedule::command')) {
  Add-Content "$APP\routes\console.php" "`nuse Illuminate\Support\Facades\Schedule;`nSchedule::command('inspire')->everyMinute();"
}
$web = Get-Content -Raw "$APP\routes\web.php"
if (-not $web.Contains('/slow')) { Add-Content "$APP\routes\web.php" "`nRoute::get('/slow', function () { usleep(1500000); return 'slow'; });" }
Has 'laranest php artisan' (Run $LN @('php', 'artisan', '--version') $APP) 'Laravel Framework'
Run $LN @('npm', 'install', '--silent', '--no-audit', '--no-fund') $APP | Out-Null
Run $LN @('npm', 'run', 'build') $APP | Out-Null
Check 'laranest npm install + build' { Test-Path "$APP\public\build\manifest.json" }
# A reused project may keep a .env fixed by a previous run.
(Get-Content "$APP\.env") -replace '^APP_URL=.*', 'APP_URL=http://localhost' | Set-Content "$APP\.env"
New-Item -ItemType Directory -Force "$Root\apps\notlaravel" | Out-Null
Set-Content "$Root\apps\notlaravel\composer.json" '{}'

# ---------------------------------------------------------------------------
Section 'Daemon, API and security'
Check 'the daemon starts' { Start-Daemon }
Has 'health' (Req GET '/health').Text '"status":"ok"'
Eq 'foreign Host -> 403' (Code "$API/health" -HostHeader 'evil.com') 403
Eq 'POST cross-origin -> 403' (Req POST '/apps/x/stop' $null @{ Origin = 'http://evil.localhost' }).Code 403
Eq 'SPA route -> index' (Code "http://$PANEL/apps/whatever") 200
Eq 'unknown API -> 404' (Req GET '/nope').Code 404
$SYS = (Req GET '/system').Json
Eq 'configured ports' "$($SYS.http_port)/$($SYS.https_port)" "$HTTP/$HTTPS"
Eq 'proxy without errors' "$($SYS.proxy_error)" ''
Eq 'reports Windows' $SYS.os 'windows'
Eq 'a second daemon refuses to start' ((& $LN serve 2>&1 | Out-String).Contains('already running')) True

Section 'Discovery and folders'
Eq 'invalid folder rejected' (Req PATCH '/system' @{ scan_dirs = @('C:\no\existe') }).Code 422
Eq 'relative path rejected' (Req PATCH '/system' @{ scan_dirs = @('rel') }).Code 422
Req PATCH '/system' @{ scan_dirs = @("$Root\apps") } | Out-Null
$disc = (Req GET '/discover').Text | ConvertFrom-Json
Check 'discover finds the project' { $disc.path -contains $APP }
Check 'discover skips non-Laravel folders' { -not ($disc.path -like '*notlaravel*') }
$fs = (Req GET "/fs?prefix=$([uri]::EscapeDataString("$Root\apps\DE"))").Text | ConvertFrom-Json
Check 'folder autocompletion (Windows path, any case)' { $fs -contains $APP }

Section 'Detection'
$INS = (Req POST '/inspect' @{ path = $APP }).Json
Eq 'detects Laravel' $INS.detection.is_laravel True
Eq 'counts scheduled tasks' $INS.detection.scheduled_tasks 1
Eq 'recommends queue+scheduler' "$($INS.detection.recommended.queue),$($INS.detection.recommended.scheduler)" 'True,True'
Has 'rejects non-Laravel projects' (Req POST '/inspect' @{ path = "$Root\apps\notlaravel" }).Text 'does not look like a Laravel project'

Section 'Adding apps and validation'
$ADD = (Req POST '/apps' @{ path = $APP; id = 'rg-demo'; group = 'Regression'; start = $true }).Json
Eq 'add with start' $ADD.app.state 'running'
Eq 'HTTPS by default' $ADD.app.https True
Eq 'URL https' $ADD.app.url "https://rg-demo.localhost:$HTTPS"
Eq 'group saved' $ADD.app.group 'Regression'
Eq 'duplicate path -> 409' (Req POST '/apps' @{ path = $APP; id = 'rg-otra' }).Code 409
Eq 'reserved id -> 422' (Req PATCH '/apps/rg-demo' @{ id = 'laranest' }).Code 422
Eq 'invalid id -> 422' (Req PATCH '/apps/rg-demo' @{ id = 'Con Espacios' }).Code 422
Eq 'group too long -> 422' (Req PATCH '/apps/rg-demo' @{ group = ('x' * 50) }).Code 422

Section 'Proxy, HTTPS and certificates'
WaitFor 20 { Test-Path $CA } | Out-Null
Check 'the local CA exists' { (Get-Item $CA).Length -gt 0 }
WaitFor 30 { (Code "https://rg-demo.localhost:$HTTPS/") -eq '200' } | Out-Null
Eq 'HTTPS verified with the CA' (Code "https://rg-demo.localhost:$HTTPS/") 200
Eq 'http -> https (308, keeps the path)' (& curl.exe -s -o NUL -w '%{http_code} %{redirect_url}' "http://rg-demo.localhost:$HTTP/a/b?c=1") "308 https://rg-demo.localhost:$HTTPS/a/b?c=1"
Eq 'static file' (Code "https://rg-demo.localhost:$HTTPS/robots.txt") 200
Eq 'unknown domain -> 404' (Code "http://nada.localhost:$HTTP/") 404
Eq 'panel via laranest.localhost' (Code "http://laranest.localhost:$HTTP/api/health") 200
WaitFor 60 { Test-Path "$Data\ca\bundle.pem" } | Out-Null
# curl with --resolve: Windows Server doesn't resolve *.localhost like Windows 10/11 do.
Set-Content "$Root\trust.php" @"
<?php
`$c = curl_init('https://rg-demo.localhost:$HTTPS/');
curl_setopt_array(`$c, [CURLOPT_RESOLVE => ['rg-demo.localhost:${HTTPS}:127.0.0.1'], CURLOPT_RETURNTRANSFER => true]);
`$local = curl_exec(`$c) !== false ? 'local-ok' : curl_error(`$c);
`$public = @file_get_contents('https://repo.packagist.org/packages.json') !== false ? 'public-ok' : 'public-failed';
echo "`$local `$public";
"@
$trust = Run $LN @('php', "$Root\trust.php") $APP
Has "Laranest's PHP trusts Laranest's CA (bundle)" $trust 'local-ok'
Has "...and public sites (Mozilla's CAs)" $trust 'public-ok'
$P = (Proc 'rg-demo' 'web').pid
Req PATCH '/apps/rg-demo' @{ https = $false } | Out-Null
Eq 'disabling HTTPS -> http' (Code "http://rg-demo.localhost:$HTTP/") 200
Eq 'toggling HTTPS does not restart the app' (Proc 'rg-demo' 'web').pid $P
Req PATCH '/apps/rg-demo' @{ https = $true } | Out-Null
$TR = (Req GET '/trust').Json
Eq 'trust status available' $TR.ready True
Check 'trust status covers the Windows store' { $null -ne $TR.windows }

Section 'Processes (php-cgi workers, supervisor, Job Objects)'
Eq 'web running' (Proc 'rg-demo' 'web').state 'running'
Eq 'queue running' (Proc 'rg-demo' 'queue').state 'running'
Eq 'scheduler running' (Proc 'rg-demo' 'scheduler').state 'running'
Eq "4 php-cgi workers from Laranest's PHP" (PhpCgi).Count 4
$u = "https://rg-demo.localhost:$HTTPS/slow"
$t0 = Get-Date
$slow = & curl.exe -s -Z --parallel-immediate --ssl-no-revoke --cacert $CA $u $u $u $u
$secs = ((Get-Date) - $t0).TotalSeconds
Eq 'concurrent requests are all served' "$slow" 'slowslowslowslow'
Check "4 slow requests (1.5s) run in parallel ($([Math]::Round($secs, 1))s)" { $secs -lt 4.5 }
$w = (PhpCgi)[0].ProcessId
Stop-Process -Id $w -Force
Check 'a crashed php-cgi worker is replaced' { WaitFor 15 { ((PhpCgi).Count -eq 4) -and -not ((PhpCgi).ProcessId -contains $w) } }
Eq '...and the app keeps responding' (Code "https://rg-demo.localhost:$HTTPS/") 200
Hasnt 'workers keep their own OPcache (no ASLR crashes)' (Get-Content -Raw "$Root\home\state\logs\laranest-rg-demo-web.log") 'ASLR'
$P = (Proc 'rg-demo' 'queue').pid
Stop-Process -Id $P -Force
Check 'a crashed worker restarts on its own' { WaitFor 15 { $q = Proc 'rg-demo' 'queue'; ($q.restarts -ge 1) -and ($q.state -eq 'running') } }
$P = (Proc 'rg-demo' 'queue').pid
Req POST '/apps/rg-demo/restart' @{ process = 'queue' } | Out-Null
Check 'restart a process' { (Proc 'rg-demo' 'queue').pid -ne $P }
$logs = curl.exe -s -N --max-time 3 "$API/apps/rg-demo/logs/queue"
Has 'live logs (SSE)' ($logs -join "`n") 'data: {'
Eq 'logs of an unknown process -> error' (Req GET '/apps/rg-demo/logs/nope').Code 422
Check 'logs are also written to a file' { (Get-ChildItem "$Root\home\state\logs\laranest-rg-demo-queue.log").Length -gt 0 }
Req POST '/apps/rg-demo/stop' | Out-Null
Eq 'stop -> state' (App 'rg-demo').state 'stopped'
Eq 'stopped app -> 503' (Code "https://rg-demo.localhost:$HTTPS/") 503
Eq 'stopping kills every php-cgi' (PhpCgi).Count 0
Req POST '/apps/rg-demo/start' | Out-Null
WaitFor 30 { (Code "https://rg-demo.localhost:$HTTPS/") -eq '200' } | Out-Null
Eq 'start again' (Code "https://rg-demo.localhost:$HTTPS/") 200

Section 'Warnings and .env'
$W = Warns 'rg-demo'
Has 'APP_URL mismatch' $W 'app_url'
Has 'missing storage:link' $W 'storage_link'
$fixw = (App 'rg-demo').warnings | Where-Object { $_.code -eq 'app_url' }
Has 'fix with the current value (diff)' $fixw.current.APP_URL 'http://localhost'
Eq 'invalid .env key -> 422' (Req POST '/apps/rg-demo/env' @{ values = @{ 'A=B' = '1' } }).Code 422
$P = (Proc 'rg-demo' 'queue').pid
Req POST '/apps/rg-demo/env' @{ values = @{ APP_URL = "https://rg-demo.localhost:$HTTPS" } } | Out-Null
Has '.env updated' ((Get-Content "$APP\.env") -match '^APP_URL=') "https://rg-demo.localhost:$HTTPS"
Hasnt 'the warning goes away' (Warns 'rg-demo') 'app_url'
Check 'workers restart with the new .env' { (Proc 'rg-demo' 'queue').pid -ne $P }
Copy-Item -Force "$APP\composer.lock" "$Root\composer.lock.bak"
$lock = Get-Content -Raw "$APP\composer.lock" | ConvertFrom-Json
$lock.packages += [pscustomobject]@{ name = 'acme/needs-oci8'; version = '1.0.0'; require = [pscustomobject]@{ 'ext-oci8' = '*'; 'ext-pcntl' = '*' } }
[IO.File]::WriteAllText("$APP\composer.lock", ($lock | ConvertTo-Json -Depth 20))
$ws = (App 'rg-demo').warnings
$oci = $ws | Where-Object { $_.code -eq 'ext_oci8' }
Has 'missing extension (ext-oci8) with a Windows hint' $oci.hint 'PECL'
$pcntl = $ws | Where-Object { $_.code -eq 'ext_pcntl' }
Eq 'ext-pcntl is only a warning on Windows' $pcntl.level 'warning'
Has '...explaining how to install anyway' $pcntl.hint 'ignore-platform-req'
Copy-Item -Force "$Root\composer.lock.bak" "$APP\composer.lock"
Set-Content "$APP\public\hot" 'http://127.0.0.1:1'
Has 'public/hot without a dev server' (Warns 'rg-demo') 'vite_hot_stale'
$vite = Start-Process node -ArgumentList '-e', "require('http').createServer((q,s)=>{if(q.headers.origin==='http://localhost')s.setHeader('Access-Control-Allow-Origin','http://localhost');s.end()}).listen(18999,'127.0.0.1')" -WindowStyle Hidden -PassThru
Start-Sleep 1; Set-Content "$APP\public\hot" 'http://127.0.0.1:18999'
Has 'Vite rejects the origin (CORS)' (Warns 'rg-demo') 'vite_cors'
Stop-Process -Id $vite.Id -Force; Remove-Item "$APP\public\hot"
# Services with real credentials: the e2e containers of services.sh, when a
# WSL distro runs them (WSL forwards their ports to Windows' localhost).
if ((Test-NetConnection 127.0.0.1 -Port 33901 -WarningAction SilentlyContinue).TcpTestSucceeded) {
  Copy-Item -Force "$APP\.env" "$Root\env.bak"
  $envLines = Get-Content "$APP\.env" | Where-Object { $_ -notmatch '^(DB_|REDIS_|CACHE_STORE|MAIL_)' }
  $envLines += @('DB_CONNECTION=mysql', 'DB_HOST=127.0.0.1', 'DB_PORT=33901', 'DB_DATABASE=e2e', 'DB_USERNAME=app', 'DB_PASSWORD=wrong',
    'CACHE_STORE=redis', 'REDIS_HOST=127.0.0.1', "REDIS_PORT=$RedisPort", 'REDIS_PASSWORD=bad', 'MAIL_MAILER=smtp', 'MAIL_HOST=127.0.0.1', "MAIL_PORT=$RedisPort")
  Set-Content "$APP\.env" $envLines
  Req POST '/apps/rg-demo/detect' | Out-Null
  $W = Warns 'rg-demo'
  Has 'MySQL with a wrong password' $W 'db_auth'
  Has 'Redis with a wrong password' $W 'redis_auth'
  Has 'SMTP port that is not SMTP' $W 'mail_protocol'
  Copy-Item -Force "$Root\env.bak" "$APP\.env"; Req POST '/apps/rg-demo/detect' | Out-Null
  Hasnt 'restoring the .env clears them' (Warns 'rg-demo') 'db_auth'
} else {
  Write-Host '  (e2e services not reachable on 127.0.0.1:33901: skipping the checks with real credentials)'
}
Has 'SQLite database checked (Windows path)' (Req GET '/apps/rg-demo').Text '"state":"running"'
Hasnt '...without SQLite errors' (Warns 'rg-demo') 'db_'

Section 'Vite dev server'
Req PATCH '/apps/rg-demo' @{ processes = @{ queue = $true; horizon = $false; scheduler = $true; reverb = $false; vite = $true } } | Out-Null
if (WaitFor 120 { (Proc 'rg-demo' 'vite').state -eq 'running' -and (Test-Path "$APP\public\hot") }) { Ok 'vite runs (npm via node, no .cmd)' }
else { Bad 'vite runs (npm via node, no .cmd)' (Get-Content "$Root\home\state\logs\laranest-rg-demo-vite.log" -Tail 15 | Out-String) }
Hasnt '...without CORS warnings' (Warns 'rg-demo') 'vite_cors'
Req PATCH '/apps/rg-demo' @{ processes = @{ queue = $true; horizon = $false; scheduler = $true; reverb = $false; vite = $false } } | Out-Null
Check 'disabling vite stops it and removes public/hot' { WaitFor 15 { -not (Test-Path "$APP\public\hot") } }

Section 'Commands'
function CmdDone($id, $cid) { WaitFor 90 { (Req GET "/apps/$id/commands/$cid").Json.status -ne 'running' } | Out-Null; (Req GET "/apps/$id/commands/$cid").Json }
$c = CmdDone 'rg-demo' (Req POST '/apps/rg-demo/commands' @{ line = 'php artisan --version' }).Json.id
Eq 'run a command in the project' $c.status 'ok'
Has 'its output is kept' $c.output 'Laravel Framework'
Eq 'the laranest prefix is optional' (Req POST '/apps/rg-demo/commands' @{ line = 'laranest php -v' }).Json.args[0] 'php'
Eq 'only project tooling (no uninstall) -> 422' (Req POST '/apps/rg-demo/commands' @{ line = 'uninstall' }).Code 422
Eq 'only project tooling (no cmd) -> 422' (Req POST '/apps/rg-demo/commands' @{ line = 'cmd /c del x' }).Code 422
$c = CmdDone 'rg-demo' (Req POST '/apps/rg-demo/commands' @{ line = 'php -r "exit(3);"' }).Json.id
Eq 'a failing command reports its exit code' "$($c.status) $($c.exit_code)" 'failed 3'
$cid = (Req POST '/apps/rg-demo/commands' @{ line = 'php -r "sleep(120);"' }).Json.id
Start-Sleep 2
Req POST "/apps/rg-demo/commands/$cid/stop" | Out-Null
Check 'stop a running command' { WaitFor 15 { (Req GET "/apps/rg-demo/commands/$cid").Json.status -eq 'stopped' } }
Check '...and its processes' { -not (Get-CimInstance Win32_Process -Filter "Name = 'php.exe'" | Where-Object { $_.CommandLine -like '*sleep(120)*' }) }
$fixcmd = ((App 'rg-demo').warnings | Where-Object { $_.code -eq 'storage_link' }).command
Eq 'warnings offer the command that fixes them' $fixcmd 'php artisan storage:link'
$c = CmdDone 'rg-demo' (Req POST '/apps/rg-demo/commands' @{ line = $fixcmd }).Json.id
Hasnt 'running it clears the warning' (Warns 'rg-demo') 'storage_link'
Eq 'history, newest first' (Req GET '/apps/rg-demo/commands').Json[0].line 'php artisan storage:link'

Section 'App settings: group, rename, PHP'
$P = (Proc 'rg-demo' 'web').pid
Req PATCH '/apps/rg-demo' @{ group = '  Other   group ' } | Out-Null
Eq 'group normalized' (App 'rg-demo').group 'Other group'
Eq 'changing the group does not restart' (Proc 'rg-demo' 'web').pid $P
Req PATCH '/apps/rg-demo' @{ processes = @{ queue = $false; horizon = $true; scheduler = $false; reverb = $false; vite = $false } } | Out-Null
$names = ((App 'rg-demo').procs | ForEach-Object { $_.name }) -join ','
Eq 'Horizon becomes queue:work on Windows' $names 'web,queue'
Req PATCH '/apps/rg-demo' @{ id = 'rg-renamed' } | Out-Null
WaitFor 30 { (Code "https://rg-renamed.localhost:$HTTPS/") -eq '200' } | Out-Null
Eq 'renaming serves the new subdomain' (Code "https://rg-renamed.localhost:$HTTPS/") 200
Check 'the old subdomain is no longer served' { (Code "https://rg-demo.localhost:$HTTPS/" -Insecure) -ne '200' }
Check 'renaming keeps the command history' { @((Req GET '/apps/rg-renamed/commands').Json).Count -ge 5 }
Eq 'unknown PHP -> 422' (Req PATCH '/apps/rg-renamed' @{ php = '7.0' }).Code 422
Eq '...and the app keeps running' (App 'rg-renamed').state 'running'

Section 'PHP versions'
$PHPI = (Req GET '/php').Json
Check 'lists installed versions' { @($PHPI.installed).Count -ge 1 }
Check 'lists downloadable versions (windows.php.net)' { (@($PHPI.available).Count -ge 1) -and ($PHPI.available[0].source -eq 'php.net') }
$INUSE = ($PHPI.installed | Where-Object { $_.used_by.Count -gt 0 } | Select-Object -First 1).version
Eq 'refuses to remove a version in use' (Req DELETE "/php/$INUSE").Code 422
Set-Content "$Data\php\$INUSE\VERSION" '8.0.0'
$upd = (Req GET '/php?refresh=1').Json.installed | Where-Object { $_.version -eq $INUSE }
Eq 'detects an available update' $upd.update True
Add-Content "$Data\php\$INUSE\php.ini" '; regression marker'
$UPD = Req POST "/php/$INUSE/update"
Has 'updating (apps stopped first) restarts the apps using it' $UPD.Text 'rg-renamed'
Check 'keeps the php.ini' { (Get-Content -Raw "$Data\php\$INUSE\php.ini").Contains('regression marker') }
Check 'the app keeps responding' { WaitFor 30 { (Code "https://rg-renamed.localhost:$HTTPS/") -eq '200' } }
$OTHER = ((Req GET '/php').Json.available | Select-Object -First 1).minor
if ($OTHER) {
  Eq "download another version ($OTHER)" (Req POST "/php/$OTHER").Code 200
  Check "...with Laranest's extension set" { (Get-Content -Raw "$Data\php\$OTHER\php.ini").Contains('extension=intl') }
  Eq 'remove an unused version' (Req DELETE "/php/$OTHER").Code 200
}

Section 'CLI'
Check 'laranest version' { "$(& $LN version)".Length -gt 0 }
Has 'laranest status' (& $LN status | Out-String) 'panel:'
Has 'which-php uses the app''s PHP' (Run $LN @('which-php') $APP) 'laranest'
Has 'laranest php -v' (Run $LN @('php', '-v') $APP) 'PHP 8.'
Has 'laranest composer' (Run $LN @('composer', '--version') $APP) 'Composer version'
Has 'laranest node' (Run $LN @('node', '-v') $APP) 'v'
Push-Location $APP; & $LN php -r 'exit(3);'; $code = $LASTEXITCODE; Pop-Location
Eq 'laranest php passes the exit code through' $code 3
$APPPHP = (Run $LN @('php', '-r', 'echo PHP_VERSION;') $APP).Trim()
# A script file: Windows PowerShell 5.1 mangles quotes in native arguments.
Set-Content "$Root\shim.js" 'process.stdout.write(require("child_process").execSync("php -r \"echo PHP_VERSION;\"").toString())'
Eq 'from Node, php is the project''s PHP (shims)' (Run $LN @('node', "$Root\shim.js") $APP).Trim() $APPPHP
Set-Content "$Root\pkey.php" '<?php echo openssl_pkey_new(["private_key_bits" => 2048]) ? "key" : openssl_error_string();'
Has 'OpenSSL can create keys (OPENSSL_CONF)' (Run $LN @('php', "$Root\pkey.php") $APP) 'key'
Set-Content "$Root\plainphp.php" '<?php echo shell_exec("php -r \"echo PHP_VERSION;\"");'
Eq 'plain php inside laranest php is the project''s PHP' (Run $LN @('php', "$Root\plainphp.php") $APP).Trim() $APPPHP
Check 'shims are hard links to laranest.exe' { (Test-Path "$Data\shims\php.exe") -and (Test-Path "$Data\shims\composer.exe") }
Has 'the php shim picks the app''s version' (Run "$Data\shims\php.exe" @('-r', 'echo PHP_VERSION;') $APP) '8.'
Has 'php:list' (& $LN php:list | Out-String) 'php.net'
Has 'trust --status' (& $LN trust --status 2>&1 | Out-String) 'Windows'
Hasnt 'no setup-ports on Windows' (& $LN --help | Out-String) 'setup-ports'

Section 'Ports and proxy errors'
$block = Start-Process node -ArgumentList '-e', "require('net').createServer().listen(8181,'127.0.0.1');setTimeout(()=>{},10000)" -WindowStyle Hidden -PassThru
Start-Sleep 1
$S = (Req PATCH '/system' @{ http_port = 8181 }).Json
Has 'busy port -> error with a hint' "$($S.proxy_error) $($S.proxy_hint)" 'Another program is using port 8181'
Stop-Process -Id $block.Id -Force
Eq 'same HTTP and HTTPS port -> 422' (Req PATCH '/system' @{ http_port = $HTTPS }).Code 422
Req PATCH '/system' @{ http_port = $HTTP } | Out-Null
WaitFor 20 { (Code "https://rg-renamed.localhost:$HTTPS/") -eq '200' } | Out-Null
Eq 'back to the original port' (Code "https://rg-renamed.localhost:$HTTPS/") 200

Section 'Daemon restarts: apps stop with it and come back'
Stop-Daemon
Eq 'stopping the daemon stops the apps (Job Objects)' (PhpCgi).Count 0
Check 'the daemon starts again' { Start-Daemon }
Check 'running apps start again' { WaitFor 40 { (Code "https://rg-renamed.localhost:$HTTPS/") -eq '200' } }
Eq 'config persisted (group)' (App 'rg-renamed').group 'Other group'
Check 'command history persisted' { @((Req GET '/apps/rg-renamed/commands').Json).Count -ge 5 }
Stop-Process -Id $script:Daemon.Id -Force
Check 'even if the daemon is killed, no orphans are left' { WaitFor 10 { (PhpCgi).Count -eq 0 } }
Check 'the daemon starts after being killed' { Start-Daemon }
Check '...and starts the apps again' { WaitFor 40 { (Code "https://rg-renamed.localhost:$HTTPS/") -eq '200' } }

Section 'UI (Edge headless)'
if ($PWDir) {
  $env:PW_DIR = $PWDir
  $env:CHROME = "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"
  $ui = & node "$PSScriptRoot\ui.mjs" "http://$PANEL" 2>&1 | Out-String
  if ($LASTEXITCODE -eq 0) { Ok 'UI walkthrough without errors' } else { Bad 'UI walkthrough' $ui }
  $ui -split "`n" | ForEach-Object { if ($_) { Write-Host "    $_" } }
} else {
  Write-Host '  (-PWDir not set: skipping the UI; see ui.mjs)'
}

Section 'Removing the app'
Req DELETE '/apps/rg-renamed' | Out-Null
Eq 'it is gone' @((Req GET '/apps').Json).Count 0
Eq 'its processes were stopped' (PhpCgi).Count 0
Check 'the project was not touched' { Test-Path "$APP\artisan" }

Stop-Daemon
Write-Host "`nResult: $($script:Pass) passed, $($script:Fail) failed" -ForegroundColor White
$script:Failed | ForEach-Object { Write-Host "  - $_" }
if ($script:Fail -gt 0) { exit 1 }
