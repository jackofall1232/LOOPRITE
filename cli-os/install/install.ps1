# One-command install for Windows (PowerShell). Mirrors install.sh: builds the single static Go
# binary, installs it to %LOCALAPPDATA%\l00prite\bin, puts that dir on the user PATH, initializes
# the data dir, and prints the browser-first next steps. Prefer a prebuilt binary? Build the
# release matrix with scripts/dist.sh (or grab a release produced from it) and skip this script.
$ErrorActionPreference = 'Stop'

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  Write-Error "l00prite CLI-OS is built from Go source and needs the Go toolchain (>= 1.24). Install it from https://go.dev/dl and re-run."
  exit 1
}

# This script lives in cli-os\install\ — build from cli-os\.
Set-Location (Join-Path $PSScriptRoot '..')

Write-Host 'Building the static l00prite binary (CGO disabled -> single static executable)...'
$env:CGO_ENABLED = '0'
go build -ldflags '-s -w' -o l00prite.exe ./cmd/l00prite
if ($LASTEXITCODE -ne 0) { Write-Error 'go build failed.'; exit 1 }

$binDir = Join-Path $env:LOCALAPPDATA 'l00prite\bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
Copy-Item -Force l00prite.exe (Join-Path $binDir 'l00prite.exe')

# Add the bin dir to the USER PATH once (never duplicate the entry). A brand-new Windows user
# account can have no User Path entry at all, which GetEnvironmentVariable returns as $null.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($null -eq $userPath) { $userPath = '' }
if (-not ($userPath -split ';' | Where-Object { $_ -eq $binDir })) {
  [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $binDir), 'User')
  Write-Host "Added $binDir to your user PATH — open a NEW terminal for it to take effect."
} else {
  Write-Host "$binDir is already on your user PATH."
}

& (Join-Path $binDir 'l00prite.exe') init

Write-Host @'

Installed l00prite. One step left:

  l00prite serve

then open  http://127.0.0.1:8787/  in your browser. The first-run wizard walks you
through adding a provider (your API key is validated with a real call, then stored
encrypted) and minting the gateway token your coding tools will use. After setup the
same URL is the dashboard: add providers, register repos, and prompt your models
from the Playground — no terminal needed.

Prefer to stay in the terminal? The CLI does everything the wizard does:

  l00prite provider add anthropic --key sk-ant-... --default
  l00prite repo register myrepo --root C:\path\to\repo
  l00prite token mint --project default
  l00prite serve
'@
