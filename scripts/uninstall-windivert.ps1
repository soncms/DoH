param([string]$InstallDir = $(Split-Path -Parent $PSScriptRoot))
$ErrorActionPreference = "Stop"
Remove-Item -LiteralPath (Join-Path $InstallDir "WinDivert.dll") -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath (Join-Path $InstallDir "WinDivert64.sys") -Force -ErrorAction SilentlyContinue
