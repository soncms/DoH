[CmdletBinding()]
param(
  [switch]$Sign,
  [switch]$Release,
  [string]$Version = $env:SONDNS_VERSION,
  [string]$CertificateThumbprint = $env:SONDNS_SIGN_CERT_THUMBPRINT,
  [string]$TimestampUrl = $env:SONDNS_TIMESTAMP_URL
)

$ErrorActionPreference = "Stop"
$workspace = Split-Path -Parent $PSScriptRoot
Push-Location $workspace
try {
  if ([string]::IsNullOrWhiteSpace($Version)) { $Version = "dev" }
  $signingRequested = $Sign -or $Release
  if ($Release) { $Sign = $true }
  $signtool = $null
  $certificate = $null
  if ($signingRequested) {
    $signtool = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($null -eq $signtool) { throw "Release signing requires signtool.exe (install Windows SDK and add it to PATH)" }
    if ([string]::IsNullOrWhiteSpace($CertificateThumbprint)) { throw "Release signing requires SONDNS_SIGN_CERT_THUMBPRINT or -CertificateThumbprint" }
    $CertificateThumbprint = $CertificateThumbprint.Replace(' ', '').ToUpperInvariant()
    $certificate = Get-ChildItem Cert:\CurrentUser\My | Where-Object { $_.Thumbprint -eq $CertificateThumbprint } | Select-Object -First 1
    if ($null -eq $certificate) { throw "Signing certificate $CertificateThumbprint was not found in Cert:\CurrentUser\My" }
    if ([string]::IsNullOrWhiteSpace($TimestampUrl)) { throw "Release signing requires SONDNS_TIMESTAMP_URL or -TimestampUrl" }
    try { $uri = [Uri]$TimestampUrl; if (-not $uri.IsAbsoluteUri -or $uri.Scheme -notin @('http','https')) { throw "invalid" } } catch { throw "Timestamp URL must be an absolute http(s) URL: $TimestampUrl" }
  }

  $verifyAssets = Join-Path $PSScriptRoot "verify-assets.ps1"
  & $verifyAssets

  if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw "go.exe was not found on PATH" }
  if (-not (Get-Command npm -ErrorAction SilentlyContinue)) { throw "npm was not found on PATH" }

  $dist = Join-Path $workspace "dist"
  New-Item -ItemType Directory -Force -Path $dist | Out-Null
  $serviceOutput = Join-Path $dist "SonDNS.Service.exe"
  $uiOutput = Join-Path $dist "SonDNS.exe"
  $installerOutput = Join-Path $dist "SonDNS-setup.exe"
  $portableOutput = Join-Path $dist "SonDNS-portable.zip"
  $electronDist = Join-Path $dist "electron"
  $portableRoot = Join-Path $dist "portable"

  # A running copy of the UI keeps its PE file open on Windows. Close only
  # SonDNS processes whose exact path is the artifact we are about to replace.
  Get-Process -ErrorAction SilentlyContinue | Where-Object {
    $_.Path -and [IO.Path]::GetFullPath($_.Path) -eq [IO.Path]::GetFullPath($uiOutput)
  } | ForEach-Object {
    $_.CloseMainWindow() | Out-Null
    if (-not $_.WaitForExit(3000)) { Stop-Process -Id $_.Id -Force }
  }
  foreach ($staleOutput in @($electronDist, (Join-Path $dist "electron-installer"))) {
    if (Test-Path -LiteralPath $staleOutput) { Remove-Item -LiteralPath $staleOutput -Recurse -Force }
  }

  & go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest assets/sondns.manifest -ico assets/sondns.ico -o rsrc_windows_amd64.syso
  if ($LASTEXITCODE -ne 0) { throw "Windows manifest/icon resource build failed" }
  & go test ./...
  if ($LASTEXITCODE -ne 0) { throw "Go tests failed" }
  & go vet ./...
  if ($LASTEXITCODE -ne 0) { throw "Go vet failed" }
  & go build -trimpath -ldflags "-H=windowsgui -X main.buildVersion=$Version" -o $serviceOutput .
  if ($LASTEXITCODE -ne 0) { throw "Go service build failed" }

  Push-Location (Join-Path $workspace "ui")
  try {
    & npm ci
    if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
    & npm run test:unit
    if ($LASTEXITCODE -ne 0) { throw "Frontend unit tests failed" }
    & npm run build
    if ($LASTEXITCODE -ne 0) { throw "Electron renderer build failed" }
    & npx electron-builder --win dir --config electron-builder.config.js
    if ($LASTEXITCODE -ne 0) { throw "Electron portable directory build failed" }
    & npx electron-builder --win nsis --config electron-builder.nsis.config.js
    if ($LASTEXITCODE -ne 0) { throw "Electron installer build failed" }
  } finally {
    Pop-Location
  }

  $packagedUI = Join-Path $electronDist "win-unpacked\SonDNS.exe"
  if (-not (Test-Path -LiteralPath $packagedUI)) { throw "Electron UI executable was not produced: $packagedUI" }
  Copy-Item -LiteralPath $packagedUI -Destination $uiOutput -Force
  $installerDist = Join-Path $dist "electron-installer"
  $nsisCandidate = Get-ChildItem -LiteralPath $installerDist -Filter "SonDNS-setup.exe" -File | Select-Object -First 1
  if ($null -eq $nsisCandidate) { throw "Electron installer was not produced" }
  Copy-Item -LiteralPath $nsisCandidate.FullName -Destination $installerOutput -Force

  if (Test-Path -LiteralPath $portableRoot) { Remove-Item -LiteralPath $portableRoot -Recurse -Force }
  New-Item -ItemType Directory -Force -Path $portableRoot | Out-Null
  $packagedUIDir = Join-Path $electronDist "win-unpacked"
  Get-ChildItem -LiteralPath $packagedUIDir -Force | Copy-Item -Destination $portableRoot -Recurse -Force
  if (-not (Test-Path -LiteralPath (Join-Path $portableRoot "SonDNS.exe"))) {
    throw "Portable bundle is missing SonDNS.exe"
  }
  if (-not (Test-Path -LiteralPath (Join-Path $portableRoot "resources\app.asar"))) {
    throw "Portable bundle is missing Electron resources"
  }
  Copy-Item -LiteralPath $serviceOutput -Destination (Join-Path $portableRoot "SonDNS.Service.exe") -Force
  Copy-Item -LiteralPath "config.example.json", "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "assets\runtime-sha256.json" -Destination $portableRoot -Force
  New-Item -ItemType Directory -Force -Path (Join-Path $portableRoot "assets") | Out-Null
  Copy-Item -LiteralPath "assets\runtime-sha256.json" -Destination (Join-Path $portableRoot "assets\runtime-sha256.json") -Force
  if (Test-Path -LiteralPath $portableOutput) { Remove-Item -LiteralPath $portableOutput -Force }
  Compress-Archive -Path (Join-Path $portableRoot "*") -DestinationPath $portableOutput -CompressionLevel Optimal

  $artifacts = @($uiOutput, $serviceOutput, $installerOutput, $portableOutput)
  $signedArtifacts = @($uiOutput, $serviceOutput, $installerOutput)
  $signatureRows = @()
  foreach ($artifact in $artifacts) {
    $isSignable = $signedArtifacts -contains $artifact
    if ($Sign -and $isSignable) {
      & $signtool.Source sign /sha1 $CertificateThumbprint /fd SHA256 /tr $TimestampUrl /td SHA256 $artifact
      if ($LASTEXITCODE -ne 0) { throw "Authenticode signing failed for $(Split-Path -Leaf $artifact)" }
      & $signtool.Source verify /pa /all $artifact
      if ($LASTEXITCODE -ne 0) { throw "Authenticode verification failed for $(Split-Path -Leaf $artifact)" }
    }
    if ($isSignable) {
      $signature = Get-AuthenticodeSignature -LiteralPath $artifact
      if ($Sign -and [string]$signature.Status -ne "Valid") {
        throw "Authenticode status for $(Split-Path -Leaf $artifact) is $($signature.Status), expected Valid"
      }
      $hash = (Get-FileHash -LiteralPath $artifact -Algorithm SHA256).Hash.ToLowerInvariant()
      $signatureRows += [ordered]@{
        artifact = Split-Path -Leaf $artifact
        sha256 = $hash
        status = [string]$signature.Status
        verify = if ($Sign) { [string]$signature.Status } else { "NotRun" }
        signer = if ($signature.SignerCertificate) { $signature.SignerCertificate.Subject } else { "" }
      }
    } else {
      # build-info.json is copied into the portable ZIP below; including the
      # ZIP's own final hash here would create a self-referential checksum.
      # SHA256SUMS remains the authoritative portable hash.
      $hash = if ($artifact -eq $portableOutput) { "" } else { (Get-FileHash -LiteralPath $artifact -Algorithm SHA256).Hash.ToLowerInvariant() }
      $signatureRows += [ordered]@{ artifact = Split-Path -Leaf $artifact; sha256 = $hash; status = "NotApplicable"; verify = "NotApplicable"; signer = "" }
    }
  }

  $sumLines = foreach ($artifact in $artifacts) {
    $hash = (Get-FileHash -LiteralPath $artifact -Algorithm SHA256).Hash.ToLowerInvariant()
    "{0}  {1}" -f $hash, (Split-Path -Leaf $artifact)
  }
  [IO.File]::WriteAllText((Join-Path $dist "SHA256SUMS"), (($sumLines -join "`n") + "`n"), [Text.Encoding]::ASCII)
  $buildInfo = [ordered]@{
    version = $Version
    product = "SonDNS"
    sourceRevision = (git rev-parse HEAD 2>$null)
    uiSourceRevision = "ivpn/desktop-app@07e24591a999c6e9974ae19b795d22465fdcf89d"
    signed = [bool]$Sign
    signerSubject = if ($certificate) { $certificate.Subject } else { "" }
    timestampUrl = if ($Sign) { $TimestampUrl } else { "" }
    artifacts = $signatureRows
  }
  $buildInfo | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $dist "build-info.json") -Encoding utf8
  Copy-Item -LiteralPath (Join-Path $dist "build-info.json") -Destination (Join-Path $portableRoot "build-info.json") -Force
  if (Test-Path -LiteralPath $portableOutput) { Remove-Item -LiteralPath $portableOutput -Force }
  Compress-Archive -Path (Join-Path $portableRoot "*") -DestinationPath $portableOutput -CompressionLevel Optimal
  $sumLines = foreach ($artifact in $artifacts) {
    $hash = (Get-FileHash -LiteralPath $artifact -Algorithm SHA256).Hash.ToLowerInvariant()
    "{0}  {1}" -f $hash, (Split-Path -Leaf $artifact)
  }
  [IO.File]::WriteAllText((Join-Path $dist "SHA256SUMS"), (($sumLines -join "`n") + "`n"), [Text.Encoding]::ASCII)
  Get-Content (Join-Path $dist "SHA256SUMS")
  $signatureRows | Format-Table | Out-Host
} finally {
  Pop-Location
}
