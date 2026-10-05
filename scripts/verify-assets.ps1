param(
  [string]$Root = (Split-Path -Parent $PSScriptRoot),
  [string]$Manifest = (Join-Path (Split-Path -Parent $PSScriptRoot) "assets\runtime-sha256.json")
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath $Manifest)) {
  throw "Runtime asset manifest was not found: $Manifest"
}

$document = Get-Content -LiteralPath $Manifest -Raw | ConvertFrom-Json
$properties = @($document.assets.psobject.Properties)
if ($properties.Count -eq 0) {
  throw "Runtime asset manifest is empty"
}

$expected = @{}
foreach ($property in $properties) {
  $relative = [string]$property.Name
  $want = ([string]$property.Value).ToLowerInvariant()
  if ($want -notmatch '^[0-9a-f]{64}$') {
    throw "Invalid SHA-256 for $relative"
  }
  $path = Join-Path $Root ($relative -replace '/', '\\')
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "Manifest asset is missing: $relative"
  }
  $got = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($got -ne $want) {
    throw "Asset hash mismatch: $relative expected $want got $got"
  }
  $expected[$relative] = $want
}

$assetRoot = Join-Path $Root "third_party\zapret2"
$actual = @(Get-ChildItem -LiteralPath $assetRoot -File -Recurse | ForEach-Object {
  $_.FullName.Substring($Root.Length + 1).Replace('\', '/')
} | Where-Object { $_ -ne "third_party/zapret2/LICENSE.txt" })

$expectedNames = @($expected.Keys | Sort-Object)
$actualNames = @($actual | Sort-Object)
if ((Compare-Object -ReferenceObject $expectedNames -DifferenceObject $actualNames).Count -ne 0) {
  throw "Runtime asset manifest does not match third_party/zapret2 files"
}

Write-Output ("Verified {0} runtime assets" -f $expected.Count)
