param(
  [string]$Version = "cmd-preview-2026.10.17",
  [string]$ConfigPath,
  [string]$OutputDirectory,
  [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
$workspace = Split-Path -Parent $PSScriptRoot
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $workspace 'dist\SonDNS-CMD' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
if (-not $SkipBuild) {
  & (Join-Path $PSScriptRoot 'build.ps1') -Console -Version $Version -OutputDirectory $OutputDirectory
}
$exe = Join-Path $OutputDirectory 'SonDNS.exe'
if (-not (Test-Path -LiteralPath $exe)) { throw 'Build the console EXE first' }
$data = Join-Path $OutputDirectory 'data'
New-Item -ItemType Directory -Force -Path $data | Out-Null
if ($ConfigPath) {
  $config = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
  $config.log_file = 'data/sondns.log'
  $config | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath (Join-Path $data 'sondns.json') -Encoding utf8
}
& $exe components update
if ($LASTEXITCODE -ne 0) { throw 'Official runtime installation/validation failed' }
Copy-Item -LiteralPath (Join-Path $workspace 'CONSOLE_PORTABLE.md') -Destination (Join-Path $OutputDirectory 'README.md') -Force
@'
@echo off
cd /d "%~dp0"
SonDNS.exe menu
if errorlevel 1 pause
'@ | Set-Content -LiteralPath (Join-Path $OutputDirectory 'Start.cmd') -Encoding ascii
$lock = Get-Content -LiteralPath (Join-Path $data 'components.lock.json') -Raw | ConvertFrom-Json
$metadata = Get-Content -LiteralPath (Join-Path $OutputDirectory 'build-info.json') -Raw | ConvertFrom-Json
$metadata | Add-Member -NotePropertyName runtime -NotePropertyValue $lock -Force
$metadata | ConvertTo-Json -Depth 30 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'build-info.json') -Encoding utf8
& $exe components verify
if ($LASTEXITCODE -ne 0) { throw 'Packaged runtime integrity check failed' }
& $exe doctor
if ($LASTEXITCODE -ne 0) { throw 'Packaged runtime preflight failed' }
Write-Output "Portable folder ready: $OutputDirectory"
