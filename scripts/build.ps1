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

  if ($signingRequested) {
    if ([string]::IsNullOrWhiteSpace($CertificateThumbprint)) {
      throw "Signing requires SONDNS_SIGN_CERT_THUMBPRINT or -CertificateThumbprint"
    }
    $CertificateThumbprint = $CertificateThumbprint.Replace(' ', '').ToUpperInvariant()
    $certificate = Get-ChildItem Cert:\CurrentUser\My |
      Where-Object { $_.Thumbprint -eq $CertificateThumbprint } |
      Select-Object -First 1
    if ($null -eq $certificate) {
      throw "Signing certificate $CertificateThumbprint was not found in Cert:\CurrentUser\My"
    }
    $signtool = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($null -eq $signtool) {
      throw "signtool.exe was not found on PATH; install the Windows SDK or add signtool to PATH"
    }
    if ([string]::IsNullOrWhiteSpace($TimestampUrl)) {
      $TimestampUrl = "http://timestamp.digicert.com"
    }
  }

  & (Join-Path $PSScriptRoot "verify-assets.ps1")
  go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest assets/sondns.manifest -ico assets/sondns.ico -o rsrc_windows_amd64.syso
  if ($LASTEXITCODE -ne 0) { throw "Windows manifest/icon resource build failed" }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw "Tests failed" }
  go vet ./...
  if ($LASTEXITCODE -ne 0) { throw "Vet failed" }

  $dist = Join-Path $workspace "dist"
  New-Item -ItemType Directory -Force -Path $dist | Out-Null
  $output = Join-Path $dist "SonDNS.exe"

  # Keep normal Go metadata in release builds. Stripping it makes unsigned
  # Windows binaries harder for reputation systems to identify as software.
  go build -trimpath -ldflags "-H=windowsgui" -o $output .
  if ($LASTEXITCODE -ne 0) { throw "Windows build failed" }

  if ($Sign) {
    & $signtool.Source sign /sha1 $CertificateThumbprint /fd SHA256 /tr $TimestampUrl /td SHA256 $output
    if ($LASTEXITCODE -ne 0) { throw "Authenticode signing failed" }
    & $signtool.Source verify /pa /all $output
    if ($LASTEXITCODE -ne 0) { throw "Authenticode verification failed" }
  }

  $signature = Get-AuthenticodeSignature -LiteralPath $output
  if ($Release -and $signature.Status -ne "Valid") {
    throw "Release artifact is not Authenticode-valid: $($signature.Status)"
  }
  $hash = (Get-FileHash -LiteralPath $output -Algorithm SHA256).Hash.ToLowerInvariant()
  $sumLine = "{0}  {1}" -f $hash, (Split-Path -Leaf $output)
  [IO.File]::WriteAllText((Join-Path $dist "SHA256SUMS"), $sumLine + "`n", [Text.Encoding]::ASCII)
  $buildInfo = [ordered]@{
    version = $Version
    artifact = (Split-Path -Leaf $output)
    sha256 = $hash
    signatureStatus = [string]$signature.Status
    signer = if ($signature.SignerCertificate) { $signature.SignerCertificate.Subject } else { "" }
    timestampUrl = if ($Sign) { $TimestampUrl } else { "" }
    release = [bool]$Release
  }
  $buildInfo | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $dist "build-info.json") -Encoding utf8
  $signature | Select-Object Status, @{Name="Signer"; Expression={ $_.SignerCertificate.Subject }}
  Get-FileHash -LiteralPath $output -Algorithm SHA256
} finally {
  Pop-Location
}
