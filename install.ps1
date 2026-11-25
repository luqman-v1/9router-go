<#
.SYNOPSIS
  9router-go one-line installer for Windows: downloads the latest release
  .exe from GitHub, installs it, and prints next steps.

.DESCRIPTION
  Run in PowerShell (no admin needed):

    irm https://raw.githubusercontent.com/luqman-v1/9router-go/main/install.ps1 | iex

  Optional overrides (set before piping, or pass when saved locally):

    $env:VERSION = "exp"     # newest experimental pre-release; default: latest
    $env:BINDIR  = "C:\Tools" # default: $env:LOCALAPPDATA\9router-go
#>
[CmdletBinding()]
param(
  [string]$Version = $env:VERSION,
  [string]$InstallDir = $env:BINDIR
)

$ErrorActionPreference = 'Stop'

$Repo = 'luqman-v1/9router-go'
if ([string]::IsNullOrWhiteSpace($Version)) { $Version = 'latest' }
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
  $InstallDir = Join-Path $env:LOCALAPPDATA '9router-go'
}

function Write-Info($msg) { Write-Host "[9router-go] $msg" -ForegroundColor Green }

# Windows release asset is amd64-only; refuse ARM with a clear message.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') {
  throw "Unsupported architecture: $arch (Windows asset is amd64-only; see https://github.com/$Repo/releases/latest)"
}

$Asset = '9router-go-windows-amd64.exe'
if ($Version -eq 'latest') {
  $Url = "https://github.com/$Repo/releases/latest/download/$Asset"
} elseif ($Version -eq 'exp' -or $Version -eq 'experimental') {
  # GitHub's releases/latest skips pre-releases. Resolve the newest one by
  # publish date (the API orders by created_at, which can put a late-published
  # old draft first).
  Write-Info "resolving newest experimental pre-release ..."
  $releases = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=100"
  $exp = $releases | Where-Object { $_.prerelease } |
    Sort-Object -Property published_at -Descending | Select-Object -First 1
  if (-not $exp) { throw "No experimental pre-release found — see https://github.com/$Repo/releases" }
  Write-Info "newest experimental release: $($exp.tag_name)"
  $Url = "https://github.com/$Repo/releases/download/$($exp.tag_name)/$Asset"
} else {
  $Url = "https://github.com/$Repo/releases/download/$Version/$Asset"
}

if (Get-Process -Name '9router-go' -ErrorAction SilentlyContinue) {
  throw '9router-go is currently running — stop it first (Ctrl+C in its terminal), then re-run the installer.'
}

if (-not (Test-Path $InstallDir)) {
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}
$Dest = Join-Path $InstallDir '9router-go.exe'
$Tmp = Join-Path $env:TEMP '9router-go-download.exe'

Write-Info "downloading $Url ..."
Invoke-WebRequest -Uri $Url -OutFile $Tmp -UseBasicParsing
Move-Item -Path $Tmp -Destination $Dest -Force

# Add install dir to the *user* PATH so `9router-go` works in new terminals.
$currentUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$newPath = $null
if ([string]::IsNullOrWhiteSpace($currentUserPath)) {
  $newPath = $InstallDir
} elseif ((($currentUserPath -split ';')) -notcontains $InstallDir) {
  $newPath = $currentUserPath.TrimEnd(';') + ';' + $InstallDir
}
if ($newPath) {
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  Write-Info "added $InstallDir to your user PATH (restart the terminal to use it)"
}
