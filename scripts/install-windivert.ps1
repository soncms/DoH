param(
  [string]$Version = "2.2.2",
  [string]$InstallDir = $(Split-Path -Parent $PSScriptRoot)
)

$ErrorActionPreference = "Stop"
$zip = Join-Path $env:TEMP ("WinDivert-{0}.zip" -f $Version)
$extract = Join-Path $env:TEMP ("WinDivert-{0}" -f $Version)
$url = "https://github.com/basil00/WinDivert/releases/download/v{0}/WinDivert-{0}-A.zip" -f $Version

Write-Host "Downloading $url"
Invoke-WebRequest -Uri $url -OutFile $zip
if (Test-Path $extract) { Remove-Item -LiteralPath $extract -Recurse -Force }
Expand-Archive -LiteralPath $zip -DestinationPath $extract
$root = Join-Path $extract ("WinDivert-{0}-A" -f $Version)
Copy-Item -LiteralPath (Join-Path $root "x64\WinDivert.dll") -Destination (Join-Path $InstallDir "WinDivert.dll") -Force
Copy-Item -LiteralPath (Join-Path $root "x64\WinDivert64.sys") -Destination (Join-Path $InstallDir "WinDivert64.sys") -Force
Write-Host "Installed WinDivert files next to SonDNS.exe in $InstallDir"
