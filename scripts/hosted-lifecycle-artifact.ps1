# Helpers only: importing this file never starts the application or a fixture.
function Resolve-HostedLifecycleArtifact {
  param([string]$RequestedVersion = '', [string]$ExpectedSHA256 = '', [Parameter(Mandatory)][string]$DefaultVersion)
  $hasVersion = -not [string]::IsNullOrEmpty($RequestedVersion)
  $hasHash = -not [string]::IsNullOrEmpty($ExpectedSHA256)
  if ($hasVersion -ne $hasHash) { throw 'Provide both artifact_version and expected_exe_sha256, or leave both empty' }
  $version = if ($hasVersion) { $RequestedVersion } else { $DefaultVersion }
  if ($version -cnotmatch '\A[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}\z') { throw 'Invalid lifecycle artifact version' }
  if ($hasHash -and $ExpectedSHA256 -cnotmatch '\A[0-9a-fA-F]{64}\z') { throw 'Expected EXE SHA-256 must contain exactly 64 hexadecimal characters' }
  [pscustomobject]@{ Version = $version; ExpectedSHA256 = ([string]$ExpectedSHA256).ToLowerInvariant(); ExactArtifactRequested = $hasHash }
}

function Assert-HostedLifecycleArtifact {
  param(
    [Parameter(Mandatory)][string]$ApplicationDirectory,
    [Parameter(Mandatory)][string]$SourceManifest,
    [Parameter(Mandatory)][string]$SourceCommit,
    [Parameter(Mandatory)][string]$Version,
    [string]$ExpectedSHA256 = '',
    [Parameter(Mandatory)][string]$ReportPath
  )
  $ErrorActionPreference = 'Stop'
  Set-StrictMode -Version Latest
  if (Test-Path -LiteralPath $ReportPath) { throw 'Artifact report already exists; do not reuse prior results' }
  # Revalidate at the execution boundary, even if the caller resolved inputs.
  $resolved = Resolve-HostedLifecycleArtifact -RequestedVersion $(if ($ExpectedSHA256 -cne '') { $Version } else { '' }) -ExpectedSHA256 $ExpectedSHA256 -DefaultVersion $Version
  if ($SourceCommit -cnotmatch '\A[0-9a-f]{40}\z') { throw 'Expected source commit must be a full Git SHA-1' }
  $info = Get-Content -LiteralPath (Join-Path $ApplicationDirectory 'build-info.json') -Raw | ConvertFrom-Json
  $manifest = Get-Content -LiteralPath $SourceManifest -Raw | ConvertFrom-Json
  $actualHash = (Get-FileHash -LiteralPath (Join-Path $ApplicationDirectory 'SonDNS.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
  $manifestHash = (Get-FileHash -LiteralPath $SourceManifest -Algorithm SHA256).Hash.ToLowerInvariant()
  $metadataValid = $info.version -ceq $resolved.Version -and $manifest.version -ceq $resolved.Version -and
    $info.edition -ceq 'portable-gui' -and $info.sha256 -ceq $actualHash -and
    $info.sourceManifestSha256 -ceq $manifestHash -and $info.sourceCommit -ceq $SourceCommit -and
    $manifest.sourceCommit -ceq $SourceCommit -and $info.sourceDirty -is [bool] -and
    $info.sourceDirty -eq $false -and $manifest.sourceDirty -is [bool] -and $manifest.sourceDirty -eq $false
  $hashMatched = $resolved.ExactArtifactRequested -and $actualHash -ceq $resolved.ExpectedSHA256
  $passed = $metadataValid -and (-not $resolved.ExactArtifactRequested -or $hashMatched)
  [ordered]@{
    schemaVersion = 1; checkedUTC = [DateTime]::UtcNow.ToString('o')
    version = $resolved.Version; expectedSHA256 = $resolved.ExpectedSHA256; actualSHA256 = $actualHash
    sourceCommit = $SourceCommit; sourceManifestSHA256 = $manifestHash
    exactArtifactRequested = $resolved.ExactArtifactRequested; exactArtifactHashMatched = $hashMatched
    buildMetadataValidated = $metadataValid; passed = $passed; lifecycleStartedByGate = $false
  } | ConvertTo-Json | Set-Content -LiteralPath $ReportPath -Encoding utf8
  if (-not $passed) { throw "Lifecycle artifact identity did not match; no lifecycle test may run. Inspect $ReportPath" }
}
