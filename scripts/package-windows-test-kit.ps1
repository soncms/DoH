param(
  [Parameter(Mandatory)][string]$ApplicationDirectory,
  [Parameter(Mandatory)][string]$OutputDirectory,
  [string]$Root = (Split-Path -Parent $PSScriptRoot)
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$Root = [IO.Path]::GetFullPath($Root)
$ApplicationDirectory = [IO.Path]::GetFullPath($ApplicationDirectory)
$sourceManifest = Join-Path $Root 'source-package.json'
if (-not (Test-Path -LiteralPath $sourceManifest -PathType Leaf)) { throw 'Build the test kit from the actual extracted verified source ZIP' }
& (Join-Path $Root 'scripts/verify-source.ps1') -Root $Root -AllowBuildOutputs
if (-not $?) { throw 'Source verification failed' }
$info = Get-Content -LiteralPath (Join-Path $ApplicationDirectory 'build-info.json') -Raw | ConvertFrom-Json
$sourceHash = (Get-FileHash -LiteralPath $sourceManifest -Algorithm SHA256).Hash.ToLowerInvariant()
if ($sourceHash -ne $info.sourceManifestSha256) { throw 'Application and test harness must use the same source manifest' }
$app = Join-Path $ApplicationDirectory 'SonDNS.exe'
if ((Get-FileHash -LiteralPath $app -Algorithm SHA256).Hash.ToLowerInvariant() -ne $info.sha256) { throw 'Application hash differs from build-info.json' }
if ($info.version -notmatch '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$') { throw 'Invalid version label' }
$stage = Join-Path ([IO.Path]::GetFullPath($OutputDirectory)) "SonDNS-clean-windows-tests-$($info.version)"
if ((Test-Path -LiteralPath $stage) -or (Test-Path -LiteralPath "$stage.zip")) { throw 'Test kit output already exists' }
New-Item -ItemType Directory -Path $stage | Out-Null
Copy-Item -LiteralPath $app -Destination (Join-Path $stage 'SonDNS.exe')
Copy-Item -LiteralPath (Join-Path $Root 'scripts/run-clean-windows-tests.ps1') -Destination $stage
Copy-Item -LiteralPath (Join-Path $Root 'scripts/ipv6-test-fixture.ps1') -Destination $stage
Copy-Item -LiteralPath (Join-Path $Root 'docs/CLEAN_WINDOWS_TESTS.md') -Destination (Join-Path $stage 'README.md')
Copy-Item -LiteralPath (Join-Path $Root 'docs/CLEAN_WINDOWS_TESTS.md') -Destination $stage
Copy-Item -LiteralPath (Join-Path $Root 'docs/RELEASE_TEST_PLAN.md') -Destination $stage
foreach ($name in @('LICENSE','NOTICE','THIRD_PARTY_NOTICES.md')) { Copy-Item -LiteralPath (Join-Path $Root $name) -Destination $stage }
Copy-Item -LiteralPath (Join-Path $Root 'licenses') -Destination $stage -Recurse
Push-Location $Root
try {
  go test -c -buildvcs=false -trimpath -o (Join-Path $stage 'SonDNS.tests.exe') .
  if ($LASTEXITCODE -ne 0) { throw 'Test harness build failed' }
} finally { Pop-Location }
$files = @(Get-ChildItem -LiteralPath $stage -File -Recurse | ForEach-Object {
  [ordered]@{path=[IO.Path]::GetRelativePath($stage,$_.FullName).Replace('\','/');sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()}
} | Sort-Object { $_.path })
[ordered]@{version=$info.version;sourceManifestSha256=$sourceHash;goVersion=$info.goVersion;applicationSha256=$info.sha256;files=$files;cleanVMLifecycleTested=$false} |
  ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $stage 'test-kit.json') -Encoding UTF8
[IO.Compression.ZipFile]::CreateFromDirectory($stage,"$stage.zip",[IO.Compression.CompressionLevel]::Optimal,$false)
$archiveHash=(Get-FileHash -LiteralPath "$stage.zip" -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText("$stage.zip.sha256",("{0}  {1}`n" -f $archiveHash,[IO.Path]::GetFileName("$stage.zip")),[Text.Encoding]::ASCII)
Write-Output "Test kit: $stage.zip"
Write-Output "SHA-256: $archiveHash"
Write-Output 'Prepared only; a compiled harness and a preflight report do not establish a clean-VM lifecycle pass.'
