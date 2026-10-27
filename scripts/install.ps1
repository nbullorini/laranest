# Laranest installer for Windows: downloads laranest.exe and installs it for
# the current user (no administrator rights): it lives in
# %LOCALAPPDATA%\Programs\Laranest, starts when you sign in and is added to
# your PATH. PHP, Composer and Node are downloaded on demand.
#   irm https://github.com/nbullorini/laranest/releases/latest/download/install.ps1 | iex
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is very slow with the progress bar
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'nbullorini/laranest'
$version = if ($env:LARANEST_VERSION) { $env:LARANEST_VERSION } else { 'latest' }
$asset = 'laranest-windows-amd64.exe' # also runs on ARM64 Windows (x64 emulation)

function Say($msg) { Write-Host "> $msg" -ForegroundColor Red }

if (-not [Environment]::Is64BitOperatingSystem) { throw 'Laranest needs 64-bit Windows.' }
if ($env:LARANEST_DOWNLOAD_URL) {
  $base = $env:LARANEST_DOWNLOAD_URL.TrimEnd('/') # a mirror (or a local test server)
} elseif ($version -eq 'latest') {
  $base = "https://github.com/$repo/releases/latest/download"
} else {
  $base = "https://github.com/$repo/releases/download/$version"
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('laranest-' + [guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
  Say "Downloading $base/$asset"
  try {
    Invoke-WebRequest -UseBasicParsing "$base/$asset" -OutFile "$tmp\$asset"
    Invoke-WebRequest -UseBasicParsing "$base/SHA256SUMS" -OutFile "$tmp\SHA256SUMS"
  } catch {
    # Private repo (pre-release testing): fall back to an authenticated gh CLI.
    if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
      throw "could not download $base/$asset (if the repo is private, install and log into the gh CLI)"
    }
    Say 'Public download failed; using gh'
    $tag = @()
    if ($version -ne 'latest') { $tag = @($version) }
    gh release download @tag -R $repo -p $asset -p SHA256SUMS -D $tmp --clobber
    if ($LASTEXITCODE -ne 0) { throw 'gh release download failed' }
  }

  $line = Get-Content "$tmp\SHA256SUMS" | Where-Object { $_ -match " $([regex]::Escape($asset))$" } | Select-Object -First 1
  $want = ("$line" -split '\s+')[0]
  $got = (Get-FileHash "$tmp\$asset" -Algorithm SHA256).Hash
  if (-not $want -or $want -ne $got) { throw "checksum mismatch for $asset" }

  Say 'Installing Laranest for your user'
  & "$tmp\$asset" install
  if ($LASTEXITCODE -ne 0) { throw 'laranest install failed' }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# Usable in this terminal too, not only in new ones.
$dir = Join-Path $env:LOCALAPPDATA 'Programs\Laranest'
if (($env:Path -split ';') -notcontains $dir) { $env:Path = "$dir;$env:Path" }

Write-Host @'

Next steps:
  * Open the panel and add your projects. Missing PHP versions are downloaded
    automatically (no administrator rights).
  * Run commands with each project's PHP:  laranest php artisan ...  /  laranest composer ...
  * Optional: make plain `php` and `composer` do that too:  laranest shims enable
'@
