param(
  [switch]$Sign,
  [switch]$Release,
  [switch]$Console,
  [string]$OutputDirectory,
  [string]$Version = $env:SONDNS_VERSION,
  [string]$CertificateThumbprint = $env:SONDNS_SIGN_CERT_THUMBPRINT,
  [string]$TimestampUrl = $env:SONDNS_TIMESTAMP_URL
)

$ErrorActionPreference = "Stop"
$workspace = Split-Path -Parent $PSScriptRoot
Push-Location $workspace
try {
  if ([string]::IsNullOrWhiteSpace($Version)) { $Version = "dev" }
  $sourceManifest = Join-Path $workspace 'source-package.json'
  if (Test-Path -LiteralPath $sourceManifest) {
    & (Join-Path $PSScriptRoot 'verify-source.ps1') -Root $workspace -AllowBuildOutputs
    if (-not $?) { throw 'Source package was modified; verify the changes and remove its old source-package.json before building modified sources' }
  }
  $signingRequested = $Sign -or $Release
  if ($Release) { $Sign = $true }
  if ($Release) {
    $nativeLock = Get-Content -LiteralPath (Join-Path $workspace 'assets/native-source-lock.json') -Raw | ConvertFrom-Json
    if ($nativeLock.correspondingSourceReady -ne $true) {
      throw "Public release is blocked by incomplete native corresponding source: $($nativeLock.status). Use an ordinary preview build while completing RELEASING.md."
    }
  }

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
  if (-not $Console) {
    & (Join-Path $PSScriptRoot "generate-licenses.ps1")
  }
  # Keep the resource compiler in go.mod so source packages can vendor it.
  go tool rsrc -arch amd64 -manifest assets/sondns.manifest -ico assets/sondns.ico -o rsrc_windows_amd64.syso
  if ($LASTEXITCODE -ne 0) { throw "Windows manifest/icon resource build failed" }
  $tagArgs = @()
  if ($Console) { $tagArgs = @('-tags', 'console') }
  go test @tagArgs -timeout 2m ./...
  if ($LASTEXITCODE -ne 0) { throw "Tests failed" }
  go vet @tagArgs ./...
  if ($LASTEXITCODE -ne 0) { throw "Vet failed" }

  $dist = if ($OutputDirectory) { [IO.Path]::GetFullPath($OutputDirectory) } elseif ($Console) { Join-Path $workspace "dist\SonDNS-CMD" } else { Join-Path $workspace "dist\SonDNS-Portable" }
  New-Item -ItemType Directory -Force -Path $dist | Out-Null
  $output = Join-Path $dist "SonDNS.exe"

  # Keep normal Go metadata in release builds. Stripping it makes unsigned
  # Windows binaries harder for reputation systems to identify as software.
  $linkFlags = if ($Console) { "-X sondns/internal/buildinfo.Version=$Version" } else { "-H=windowsgui -X sondns/internal/buildinfo.Version=$Version" }
  $vcsArgs = @()
  if ((Test-Path -LiteralPath $sourceManifest) -or -not (Test-Path -LiteralPath (Join-Path $workspace '.git'))) { $vcsArgs = @('-buildvcs=false') }
  go build @tagArgs @vcsArgs -trimpath -ldflags $linkFlags -o $output ./cmd/sondns
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
  if (Test-Path -LiteralPath $sourceManifest) {
    $sourceInfo = Get-Content -LiteralPath $sourceManifest -Raw | ConvertFrom-Json
    $sourceCommit = $sourceInfo.sourceCommit
    $sourceDirty = $sourceInfo.sourceDirty
  } elseif (Test-Path -LiteralPath (Join-Path $workspace '.git')) {
    $sourceCommit = (git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Could not identify source commit" }
    $sourceStatus = @(git status --porcelain --untracked-files=normal)
    $sourceDirty = ($sourceStatus.Count -gt 0)
  } else {
    # Modified source archives may be rebuilt without Git after their original
    # integrity manifest is removed. Do not borrow a parent repo's provenance.
    $sourceCommit = ''
    $sourceDirty = $true
  }
  $goVersion = (go env GOVERSION).Trim()
  if ($LASTEXITCODE -ne 0) { throw "Could not identify Go toolchain" }
  # Record modules actually linked into this artifact, including the local Walk replacement.
  $binaryMetadata = @(go version -m $output)
  if ($LASTEXITCODE -ne 0) { throw "Could not read artifact dependency metadata" }
  $buildInfo = [ordered]@{
    version = $Version
    artifact = (Split-Path -Leaf $output)
    sha256 = $hash
    signatureStatus = [string]$signature.Status
    signer = if ($signature.SignerCertificate) { $signature.SignerCertificate.Subject } else { "" }
    timestampUrl = if ($Sign) { $TimestampUrl } else { "" }
    release = [bool]$Release
    edition = if ($Console) { "console" } else { "portable-gui" }
    builtAtUtc = [DateTime]::UtcNow.ToString("o")
    sourceCommit = $sourceCommit
    sourceDirty = $sourceDirty
    sourceManifestSha256 = if (Test-Path -LiteralPath $sourceManifest) { (Get-FileHash -LiteralPath $sourceManifest -Algorithm SHA256).Hash.ToLowerInvariant() } else { "" }
    goVersion = $goVersion
    binaryMetadata = $binaryMetadata
  }
  $buildInfo | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $dist "build-info.json") -Encoding utf8
  $signature | Select-Object Status, @{Name="Signer"; Expression={ $_.SignerCertificate.Subject }}
  Get-FileHash -LiteralPath $output -Algorithm SHA256
} finally {
  Pop-Location
}
