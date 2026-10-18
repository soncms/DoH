param(
  [string]$Root = (Split-Path -Parent $PSScriptRoot),
  [string]$OutputDirectory,
  [string]$NativeSourceCacheDirectory,
  [ValidatePattern('^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$')]
  [string]$Version = 'source-preview'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$Root = [IO.Path]::GetFullPath($Root)
if (-not $NativeSourceCacheDirectory) {
  # Keep source builds independent of the availability of historical mirrors.
  # The collector still verifies every cached archive and its source evidence.
  foreach ($name in @('native-source-cache','native-sources')) {
    $candidate = Join-Path $Root $name
    if (Test-Path -LiteralPath $candidate -PathType Container) {
      $NativeSourceCacheDirectory = $candidate
      break
    }
  }
}
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $Root 'dist/source' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$stage = Join-Path $OutputDirectory "SonDNS-source-$Version"
$archive = "$stage.zip"
if ((Test-Path -LiteralPath $stage) -or (Test-Path -LiteralPath $archive) -or (Test-Path -LiteralPath "$archive.sha256")) {
  throw "Source output already exists; choose a new version/output directory: $stage"
}

function Assert-PlainPath([string]$Path) {
  $item = Get-Item -LiteralPath $Path -Force
  if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Source links are not allowed: $Path" }
}

Assert-PlainPath $Root
$selected = [Collections.Generic.List[string]]::new()
foreach ($file in Get-ChildItem -LiteralPath $Root -Filter '*.go' -File) {
  $selected.Add($file.Name)
}
foreach ($name in @('.gitattributes','go.mod','go.sum','LICENSE','NOTICE','README.md','PORTABLE.md',
  'THIRD_PARTY_NOTICES.md','RUNTIME_PROVENANCE.md','RELEASING.md','SECURITY.md',
  'CONTRIBUTING.md','PRIVACY.md','config.example.json')) {
  if (-not (Test-Path -LiteralPath (Join-Path $Root $name) -PathType Leaf)) { throw "Required source is missing: $name" }
  $selected.Add($name)
}
# Explicit roots omit .git, credentials, editor state, logs, local configuration,
# agent memory, build outputs and historical local investigation reports.
foreach ($subdir in @('cmd','internal','assets','scripts','docs','licenses','third_party/walk','third_party/dnscrypt','third_party/dnsproxy','third_party/zapret2','.github/workflows')) {
  $dir = Join-Path $Root $subdir
  if (-not (Test-Path -LiteralPath $dir -PathType Container)) { throw "Required source directory is missing: $subdir" }
  Assert-PlainPath $dir
  foreach ($item in Get-ChildItem -LiteralPath $dir -Recurse -Force) {
    Assert-PlainPath $item.FullName
    $relative = [IO.Path]::GetRelativePath($Root, $item.FullName).Replace('\','/')
    if ($relative -match '(^|/)(\.git|\.env[^/]*|data|dist|node_modules)(/|$)' -or $relative -match '\.(pfx|p12|pem|key)$') {
      throw "Unexpected private/generated content in source directory: $relative"
    }
    if (-not $item.PSIsContainer) { $selected.Add($relative) }
  }
}
New-Item -ItemType Directory -Path $stage -Force | Out-Null
foreach ($relative in @($selected | Sort-Object -Unique)) {
  $source = Join-Path $Root $relative
  Assert-PlainPath $source
  $target = Join-Path $stage $relative
  New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force | Out-Null
  Copy-Item -LiteralPath $source -Destination $target
}

Push-Location $stage
try {
  # Work from the copied module, so Go cannot modify the live checkout. The
  # local Walk replacement is copied verbatim before vendoring.
  go mod vendor
  if ($LASTEXITCODE -ne 0) { throw 'Could not vendor application and build-tool dependencies' }
  & (Join-Path $stage 'scripts/generate-licenses.ps1') -Root $stage
  if (-not $?) { throw 'Could not collect vendored dependency licenses' }
} finally { Pop-Location }
& (Join-Path $stage 'scripts/collect-native-sources.ps1') -Root $stage -OutputDirectory (Join-Path $stage 'native-sources') -CacheDirectory $NativeSourceCacheDirectory
if (-not $?) { throw 'Could not collect pinned native source evidence' }
$nativeLock = Get-Content -LiteralPath (Join-Path $stage 'assets/native-source-lock.json') -Raw | ConvertFrom-Json

$commit = ''
$dirty = $true
$originalManifest = Join-Path $Root 'source-package.json'
if (Test-Path -LiteralPath $originalManifest) {
  $previous = Get-Content -LiteralPath $originalManifest -Raw | ConvertFrom-Json
  $commit, $dirty = $previous.sourceCommit, $previous.sourceDirty
} else {
  Push-Location $Root
  try {
    $gitRoot = git rev-parse --show-toplevel
    if ($LASTEXITCODE -ne 0 -or [IO.Path]::GetFullPath($gitRoot) -ne $Root) { throw 'Source must be a repository root or verified source package' }
    $commit = (git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Could not identify source commit' }
    $dirty = @(git status --porcelain --untracked-files=normal).Count -gt 0
  } finally { Pop-Location }
}
$inventory = @(Get-ChildItem -LiteralPath $stage -File -Recurse | Sort-Object FullName | ForEach-Object {
  [ordered]@{
    path = [IO.Path]::GetRelativePath($stage, $_.FullName).Replace('\','/')
    bytes = $_.Length
    sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
  }
})
$manifest = [ordered]@{
  schemaVersion = 1
  project = 'SonDNS'
  version = $Version
  scope = 'application-go-dependencies-and-native-library-sources'
  nativeCorrespondingSource = $nativeLock.status
  sourceCommit = $commit
  sourceDirty = [bool]$dirty
  createdAtUtc = [DateTime]::UtcNow.ToString('o')
  files = $inventory
}
$manifest | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $stage 'source-package.json') -Encoding utf8
& (Join-Path $stage 'scripts/verify-source.ps1') -Root $stage
if (-not $?) { throw 'Source package verification failed' }
[IO.Compression.ZipFile]::CreateFromDirectory($stage, $archive)
$sha = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText("$archive.sha256", "$sha  $([IO.Path]::GetFileName($archive))`n", [Text.Encoding]::ASCII)
Write-Output "Source archive: $archive"
Write-Output "SHA-256: $sha"
Write-Output 'Scope: application + vendored Go/build tools + pinned WinDivert/Cygwin source, reference archives and build recipes. Source correspondence is verified; signing and manual release checks remain in RELEASING.md.'
