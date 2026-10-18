$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'hosted-lifecycle-artifact.ps1')
$count = 0
function Expect-Rejection([scriptblock]$Action) {
  $rejected = $false
  try { & $Action | Out-Null } catch { $rejected = $true }
  if (-not $rejected) { throw 'Expected artifact identity rejection' }
}
$hash = 'a' * 64
$resolved = Resolve-HostedLifecycleArtifact -RequestedVersion 'preview.10' -ExpectedSHA256 $hash.ToUpperInvariant() -DefaultVersion 'ci-1'
if ($resolved.Version -cne 'preview.10' -or $resolved.ExpectedSHA256 -cne $hash -or -not $resolved.ExactArtifactRequested) { throw 'Paired input resolution failed' }
$count++
$default = Resolve-HostedLifecycleArtifact -DefaultVersion 'ci-1'
if ($default.Version -cne 'ci-1' -or $default.ExactArtifactRequested) { throw 'Default CI input resolution failed' }
$count++
foreach ($case in @(
  @{Version='preview'; Hash=''}, @{Version=''; Hash=$hash}, @{Version='../escape'; Hash=$hash},
  @{Version='preview;exit'; Hash=$hash}, @{Version="preview`n"; Hash=$hash},
  @{Version=('x' * 81); Hash=$hash}, @{Version='preview'; Hash=('a' * 63)},
  @{Version='preview'; Hash=('g' * 64)}, @{Version='preview'; Hash=($hash + "`n")}
)) {
  Expect-Rejection { Resolve-HostedLifecycleArtifact -RequestedVersion $case.Version -ExpectedSHA256 $case.Hash -DefaultVersion 'ci-1' }
  $count++
}
$root = Join-Path ([IO.Path]::GetTempPath()) ('sondns-artifact-test-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
$manifestPath = Join-Path $root 'source-package.json'
$infoPath = Join-Path $root 'build-info.json'
$exe = Join-Path $root 'SonDNS.exe'
$report = Join-Path $root 'artifact-expectation.json'
$commit = 'b' * 40
try {
  [IO.File]::WriteAllText($exe, 'inert fixture, never executed')
  $actual = (Get-FileHash -LiteralPath $exe).Hash.ToLowerInvariant()
  @{version='preview.10';sourceCommit=$commit;sourceDirty=$false} | ConvertTo-Json | Set-Content -LiteralPath $manifestPath
  $manifestHash = (Get-FileHash -LiteralPath $manifestPath).Hash.ToLowerInvariant()
  $valid = @{version='preview.10';edition='portable-gui';sourceCommit=$commit;sourceDirty=$false;sha256=$actual;sourceManifestSha256=$manifestHash}
  foreach ($case in @('exact','default','hash-mismatch','file-changed','version-mismatch','source-mismatch','dirty','dirty-string','manifest-mismatch')) {
    [IO.File]::WriteAllText($exe, 'inert fixture, never executed')
    $info = $valid.Clone()
    $expected = $actual
    switch ($case) {
      'default' { $expected = '' }
      'hash-mismatch' { $expected = 'c' * 64 }
      'file-changed' { [IO.File]::AppendAllText($exe, ' altered bytes') }
      'version-mismatch' { $info.version = 'different' }
      'source-mismatch' { $info.sourceCommit = 'd' * 40 }
      'dirty' { $info.sourceDirty = $true }
      'dirty-string' { $info.sourceDirty = 'false' }
      'manifest-mismatch' { $info.sourceManifestSha256 = 'e' * 64 }
    }
    $info | ConvertTo-Json | Set-Content -LiteralPath $infoPath
    $action = { Assert-HostedLifecycleArtifact -ApplicationDirectory $root -SourceManifest $manifestPath -SourceCommit $commit -Version 'preview.10' -ExpectedSHA256 $expected -ReportPath $report }
    if ($case -in @('exact','default')) { & $action } else { Expect-Rejection $action }
    $result = Get-Content -LiteralPath $report -Raw | ConvertFrom-Json
    if ($result.passed -ne ($case -in @('exact','default')) -or $result.lifecycleStartedByGate) { throw "Incorrect report: $case" }
    if ($case -eq 'exact' -and -not $result.exactArtifactHashMatched) { throw 'Missing exact hash proof' }
    if ($case -eq 'default' -and ($result.exactArtifactRequested -or $result.exactArtifactHashMatched)) { throw 'Default scan must not claim an exact-artifact comparison' }
    if ($case -eq 'hash-mismatch' -and (-not $result.buildMetadataValidated -or $result.exactArtifactHashMatched)) { throw 'Hash mismatch was not distinguished from invalid metadata' }
    if ($case -eq 'file-changed' -and ($result.actualSHA256 -ceq $actual -or $result.buildMetadataValidated -or $result.exactArtifactHashMatched)) { throw 'Changed EXE bytes were trusted as build metadata' }
    Expect-Rejection $action # Prior report, including a pass, cannot be reused.
    Remove-Item -LiteralPath $report
    $count++
  }
  Remove-Item -LiteralPath $exe
  Expect-Rejection { Assert-HostedLifecycleArtifact -ApplicationDirectory $root -SourceManifest $manifestPath -SourceCommit $commit -Version 'preview.10' -ExpectedSHA256 $actual -ReportPath $report }
  if (Test-Path -LiteralPath $report) { throw 'Missing EXE must not produce a passing report' }
  $count++
} finally {
  # Known files only; no recursive delete or path derived from caller input.
  foreach ($path in @($report,$exe,$infoPath,$manifestPath)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path } }
  Remove-Item -LiteralPath $root
}
Write-Output "Hosted lifecycle artifact gate: $count cases passed (no executable or live fixture was run)"
