# Windows version of run-projects.sh: installs real open source Laravel
# projects with Laranest's own tooling (laranest composer/php/npm), registers
# them in an isolated Laranest instance, starts them and probes them over HTTPS.
#
#   test/e2e/services.sh up                      # in WSL: MySQL/PostgreSQL/Redis/Mailpit,
#                                                # reachable from Windows through localhost
#   powershell -ExecutionPolicy Bypass -File test\e2e\run-projects.ps1 -Laranest bin\laranest.exe [-Only laravel,fdemo]
#
# Databases: e2e_w_<name> must exist (run-projects.sh creates e2e_<name>; see
# the README). Rows whose database server isn't reachable are skipped.
# Results: <Root>\results\<name>.json, logs: <Root>\logs\<name>.log
param(
  [string]$Laranest = "$PSScriptRoot\..\..\bin\laranest.exe",
  [string]$Root = "$env:TEMP\laranest-e2e",
  [string[]]$Only = @(),
  [int]$Jobs = 3,
  # Hyper-V often reserves 63901 on Windows (netsh int ipv4 show excludedportrange tcp):
  # then run a second Redis on another port, e.g. 26390.
  [int]$RedisPort = 63901
)
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'

New-Item -ItemType Directory -Force "$Root\bin", "$Root\home\config", "$Root\projects", "$Root\logs", "$Root\results" | Out-Null
$Only = @($Only | ForEach-Object { $_ -split ',' } | Where-Object { $_ }) # -File passes "a,b" as one string
# A daemon left from a previous run holds the binary: stop it to update it.
try { Invoke-RestMethod -Method Post 'http://127.0.0.1:9170/api/shutdown' -TimeoutSec 5 | Out-Null; Start-Sleep 3 } catch {}
Copy-Item -Force $Laranest "$Root\bin\laranest.exe"
$LN = "$Root\bin\laranest.exe"
$env:LARANEST_HOME = "$Root\home"
$PANEL = '127.0.0.1:9170'; $HTTPS = 8570
$API = "http://$PANEL/api"
$CA = "$Root\home\state\caddy\pki\authorities\local\root.crt"
if (-not (Test-Path "$Root\home\config\config.json")) {
  Set-Content -Encoding ascii "$Root\home\config\config.json" "{`"panel_addr`":`"$PANEL`",`"http_port`":8170,`"https_port`":$HTTPS,`"domain`":`"localhost`"}"
}
try { Invoke-RestMethod "$API/health" -TimeoutSec 2 | Out-Null } catch {
  Start-Process -FilePath $LN -ArgumentList 'serve' -WindowStyle Hidden -RedirectStandardOutput "$Root\daemon.log" -RedirectStandardError "$Root\daemon.err" | Out-Null
  for ($i = 0; $i -lt 50; $i++) { try { Invoke-RestMethod "$API/health" -TimeoutSec 2 | Out-Null; break } catch { Start-Sleep -Milliseconds 300 } }
}

$reachable = @{}
foreach ($p in 33901, 54901, $RedisPort) { $reachable[$p] = (Test-NetConnection 127.0.0.1 -Port $p -WarningAction SilentlyContinue).TcpTestSucceeded }

$rows = Get-Content "$PSScriptRoot\projects.tsv" | Where-Object { $_ -and -not $_.StartsWith('#') } | ForEach-Object {
  $f = $_ -split "`t"
  [pscustomobject]@{ Name = $f[0]; Repo = $f[1]; Ref = $f[2]; DB = $f[3]; Assets = $f[4]; Kind = $f[5] }
}
if ($Only.Count -gt 0) { $rows = $rows | Where-Object { $Only -contains $_.Name } }

$project = {
  param($row, $Root, $LN, $API, $CA, $HTTPS, $reachable, $RedisPort)
  $ErrorActionPreference = 'Continue'
  $env:LARANEST_HOME = "$Root\home"
  $name = $row.Name; $id = "e2e-$name"; $dir = "$Root\projects\$name"
  $log = "$Root\logs\$name.log"; [IO.File]::WriteAllText($log, '')
  $url = "https://$id.localhost:$HTTPS"
  $steps = New-Object System.Collections.ArrayList
  function Api($method, $path, $body = $null) {
    $p = @{ Method = $method; Uri = "$API$path"; ContentType = 'application/json'; TimeoutSec = 900 }
    if ($null -ne $body) { $p.Body = ($body | ConvertTo-Json -Depth 6 -Compress) }
    try { Invoke-RestMethod @p } catch { $r = $_.ErrorDetails.Message; try { $r | ConvertFrom-Json } catch { @{ error = "$_" } } }
  }
  function Step($label, [string[]]$cmd) {
    [IO.File]::AppendAllText($log, "### ${label}: $($cmd -join ' ')`n")
    Push-Location $dir
    $exe = $cmd[0]; $rest = @(); if ($cmd.Count -gt 1) { $rest = $cmd[1..($cmd.Count - 1)] }
    & $exe @rest 2>&1 | ForEach-Object { [IO.File]::AppendAllText($log, "$_`n") }
    $code = $LASTEXITCODE
    Pop-Location
    if ($code -eq 0) { [void]$steps.Add("${label}:ok"); return $true }
    [void]$steps.Add("${label}:fail"); return $false
  }
  function SetEnv($pairs) {
    $path = "$dir\.env"
    $lines = @(); if (Test-Path $path) { $lines = @(Get-Content $path) }
    foreach ($k in $pairs.Keys) {
      $v = $pairs[$k]; if ($v -match '[ #]') { $v = "`"$v`"" }
      $i = 0; $found = $false
      for (; $i -lt $lines.Count; $i++) { if ($lines[$i] -match "^\s*#?\s*$([regex]::Escape($k))\s*=") { $lines[$i] = "$k=$v"; $found = $true; break } }
      if (-not $found) { $lines += "$k=$v" }
    }
    [IO.File]::WriteAllText($path, (($lines -join "`n") + "`n"))
  }

  if (-not (Test-Path "$dir\.git")) {
    [IO.File]::AppendAllText($log, "### clone`n")
    git -c core.longpaths=true clone -q --depth 1 -b $row.Ref "https://github.com/$($row.Repo)" $dir 2>&1 | ForEach-Object { [IO.File]::AppendAllText($log, "$_`n") }
    if ($LASTEXITCODE -eq 0) { [void]$steps.Add('clone:ok') } else { [void]$steps.Add('clone:fail') }
  }
  $inspect = Api POST '/inspect' @{ path = $dir }
  if ($row.Kind -eq 'edge') {
    $add = Api POST '/apps' @{ path = $dir; id = $id; start = $true }
    Api DELETE "/apps/$id" | Out-Null
    [pscustomobject]@{ name = $name; kind = 'edge'; add_error = $add.error; add_state = $add.app.state } |
      ConvertTo-Json -Depth 6 | Set-Content "$Root\results\$name.json"
    return
  }
  $port = @{ mysql = 33901; pgsql = 54901 }[$row.DB]
  if ($port -and -not $reachable[$port]) {
    [pscustomobject]@{ name = $name; kind = $row.Kind; skipped = "$($row.DB) not reachable" } | ConvertTo-Json | Set-Content "$Root\results\$name.json"
    return
  }

  if (-not (Test-Path "$dir\.env")) { if (Test-Path "$dir\.env.example") { Copy-Item "$dir\.env.example" "$dir\.env" } else { Set-Content "$dir\.env" '' } }
  SetEnv @{ APP_ENV = 'local'; APP_DEBUG = 'true'; APP_URL = $url; REDIS_HOST = '127.0.0.1'; REDIS_PORT = "$RedisPort"; REDIS_PASSWORD = 's3cret';
    MAIL_MAILER = 'smtp'; MAIL_HOST = '127.0.0.1'; MAIL_PORT = '10925' }
  switch ($row.DB) {
    'sqlite' { New-Item -ItemType Directory -Force "$dir\database" | Out-Null; if (-not (Test-Path "$dir\database\database.sqlite")) { Set-Content "$dir\database\database.sqlite" -NoNewline '' }
      SetEnv @{ DB_CONNECTION = 'sqlite'; DB_DATABASE = "$dir\database\database.sqlite" } }
    'mysql' { SetEnv @{ DB_CONNECTION = 'mysql'; DB_HOST = '127.0.0.1'; DB_PORT = '33901'; DB_DATABASE = "e2e_w_$name"; DB_USERNAME = 'root'; DB_PASSWORD = 'secret' } }
    'pgsql' { SetEnv @{ DB_CONNECTION = 'pgsql'; DB_HOST = '127.0.0.1'; DB_PORT = '54901'; DB_DATABASE = "e2e_w_$name"; DB_USERNAME = 'app'; DB_PASSWORD = 'secret' } }
  }

  # As Laranest's warning suggests on Windows: Unix-only extensions (Horizon's) can't be installed.
  Step 'composer' @($LN, 'composer', 'install', '--no-interaction', '--prefer-dist', '--no-progress',
    '--ignore-platform-req=ext-pcntl', '--ignore-platform-req=ext-posix') | Out-Null

  Api DELETE "/apps/$id" | Out-Null
  Api POST '/apps' @{ path = $dir; id = $id; start = $false } | Out-Null
  $fix = @{}
  foreach ($w in (Api GET "/apps/$id").app.warnings) {
    if ($w.code -eq 'app_key' -and $w.fix) { $w.fix.PSObject.Properties | ForEach-Object { $fix[$_.Name] = $_.Value } }
  }
  if ($fix.Count -gt 0) { [void]$steps.Add('applied-fix:app_key'); Api POST "/apps/$id/env" @{ values = $fix } | Out-Null }
  Step 'migrate' @($LN, 'php', 'artisan', 'migrate', '--force') | Out-Null

  if ($row.Assets.StartsWith('cmd:')) {
    # The rows are written for bash: "$LN a b && $LN c d".
    $ok = $true
    foreach ($part in ($row.Assets.Substring(4) -split ' && ')) {
      if (-not $ok) { break }
      $tokens = @($part.Trim() -split '\s+' | ForEach-Object { if ($_ -eq '$LN') { $LN } else { $_ } })
      $ok = Step 'assets' $tokens
    }
  } elseif ($row.Assets -ne 'no' -and (Test-Path "$dir\package.json")) {
    $pkg = Get-Content -Raw "$dir\package.json" | ConvertFrom-Json
    if ($pkg.scripts.build) {
      $install = 'install'; if (Test-Path "$dir\package-lock.json") { $install = 'ci' }
      if (Step 'npm' @($LN, 'npm', $install, '--no-audit', '--no-fund')) { Step 'build' @($LN, 'npm', 'run', 'build') | Out-Null }
    }
  }

  $add = Api POST "/apps/$id/start"
  $code = '000'; $final = ''
  for ($i = 0; $i -lt 60; $i++) {
    $r = & curl.exe -s -L --max-redirs 5 --ssl-no-revoke --cacert $CA -o NUL -w '%{http_code} %{url_effective}' "$url/"
    $code, $final = "$r" -split ' ', 2
    if ($code -like '2*') { break }
    Start-Sleep 1
  }
  Start-Sleep 8 # let workers crash-loop if they're going to
  $view = (Api GET "/apps/$id").app
  $html = & curl.exe -s -L --max-redirs 5 --ssl-no-revoke --cacert $CA "$url/" | Out-String
  $title = ''; if ($html -match '<title>([^<]*)') { $title = $Matches[1].Trim() }
  [pscustomobject]@{
    name = $name; kind = $row.Kind; http = $code; final = $final; title = $title; steps = @($steps)
    add_error = $add.error; php = $view.php_version; laravel = $view.detection.laravel_version
    procs = @($view.procs | ForEach-Object { [pscustomobject]@{ name = $_.name; state = $_.state; restarts = $_.restarts } })
    warnings = @($view.warnings | ForEach-Object { [pscustomobject]@{ code = $_.code; level = $_.level; message = $_.message } })
  } | ConvertTo-Json -Depth 6 | Set-Content "$Root\results\$name.json"
  Api POST "/apps/$id/stop" | Out-Null
}

$running = @()
foreach ($row in $rows) {
  while (@($running | Where-Object { $_.State -eq 'Running' }).Count -ge $Jobs) { Start-Sleep 2 }
  $running += Start-Job -Name $row.Name -ScriptBlock $project -ArgumentList $row, $Root, $LN, $API, $CA, $HTTPS, $reachable, $RedisPort
}
foreach ($j in $running) {
  Wait-Job $j | Out-Null
  Receive-Job $j -ErrorAction SilentlyContinue | Out-Null
  Write-Host "done: $($j.Name) ($($j.State))"
}
