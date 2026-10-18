param(
  [string]$Root = (Split-Path -Parent $PSScriptRoot),
  [switch]$AllowBuildOutputs
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$Root = [IO.Path]::GetFullPath($Root)
$document = Get-Content -LiteralPath (Join-Path $Root 'source-package.json') -Raw | ConvertFrom-Json
if ($document.schemaVersion -ne 1 -or $document.project -ne 'SonDNS' -or
  $document.scope -cnotin @('application-and-go-dependencies', 'application-go-dependencies-and-native-library-sources')) {
  throw 'Unsupported source manifest'
}
$expected = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
foreach ($row in $document.files) {
  $relative = [string]$row.path
  if (-not $relative -or $relative.Contains('\') -or $relative.Contains(':') -or $relative.StartsWith('/') -or
    $relative -match '(^|/)\.{1,2}(/|$)' -or $relative.Contains('//') -or $relative -eq 'source-package.json') {
    throw "Invalid source manifest path: $relative"
  }
  if (-not $expected.Add($relative)) { throw "Duplicate source path: $relative" }
  if ($row.sha256 -cnotmatch '^[0-9a-f]{64}$') { throw "Invalid source hash: $relative" }
  $path = Join-Path $Root $relative
  $file = Get-Item -LiteralPath $path -Force
  if ($file.PSIsContainer -or $file.Length -ne $row.bytes) { throw "Source size/type mismatch: $relative" }
  if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $row.sha256) {
    throw "Source hash mismatch: $relative"
  }
}
if ($expected.Count -eq 0) { throw 'Source manifest is empty' }
# Also reject unlisted inputs and links, including a directory that could make
# the hashes read bytes outside this source package.
$rootItem = Get-Item -LiteralPath $Root -Force
if ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Source root is a link' }
foreach ($file in Get-ChildItem -LiteralPath $Root -Recurse -Force) {
  if ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Source contains a link: $($file.FullName)" }
  if ($file.PSIsContainer) { continue }
  $relative = [IO.Path]::GetRelativePath($Root, $file.FullName).Replace('\','/')
  if ($AllowBuildOutputs -and ($relative -eq 'rsrc_windows_amd64.syso' -or $relative.StartsWith('dist/'))) { continue }
  if ($relative -ne 'source-package.json' -and -not $expected.Contains($relative)) {
    throw "Unlisted source file: $relative"
  }
}
Write-Output "Verified $($expected.Count) source/dependency files. Native corresponding-source status: $($document.nativeCorrespondingSource)."
