$ErrorActionPreference = "Stop"
$workspace = Split-Path -Parent $PSScriptRoot
Push-Location $workspace
try {
  go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest assets/sondns.manifest -o rsrc_windows_amd64.syso
  if ($LASTEXITCODE -ne 0) { throw "Manifest build failed" }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw "Tests failed" }
  go vet ./...
  if ($LASTEXITCODE -ne 0) { throw "Vet failed" }
  $dist = Join-Path $workspace "dist"
  New-Item -ItemType Directory -Force -Path $dist | Out-Null
  go build -trimpath -ldflags "-s -w -H=windowsgui" -o (Join-Path $dist "SonDNS.exe") .
  if ($LASTEXITCODE -ne 0) { throw "Windows build failed" }
  Get-FileHash -LiteralPath (Join-Path $dist "SonDNS.exe") -Algorithm SHA256
} finally {
  Pop-Location
}
