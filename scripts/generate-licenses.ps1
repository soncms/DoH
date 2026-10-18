param([string]$Root = (Split-Path -Parent $PSScriptRoot))
$ErrorActionPreference = 'Stop'
Push-Location $Root
try {
  $inventoryPath = Join-Path $Root 'licenses/modules.json'
  $previousInventory = @()
  if (Test-Path -LiteralPath $inventoryPath) {
    $previousInventory = @(Get-Content -LiteralPath $inventoryPath -Raw | ConvertFrom-Json)
  }
  $modules = @(go list -deps -f '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}{{end}}' ./cmd/sondns | Where-Object { $_ } | Sort-Object -Unique)
  if ($LASTEXITCODE -ne 0) { throw 'Cannot identify linked modules' }
  $rows = @()
  foreach ($line in $modules) {
    $parts = $line.Split('|', 3)
    $modulePath, $version, $directory = $parts
    # Go omits Module.Dir when building from vendor. The package source and
    # license files are included there by go mod vendor.
    $fromVendor = -not $directory
    if ($fromVendor) { $directory = Join-Path $Root "vendor/$modulePath" }
    if (-not (Test-Path -LiteralPath $directory)) { throw "Missing source for $modulePath" }
    $id = $modulePath.Replace('/', '_')
    $destination = Join-Path $Root "licenses\go\$id"
    $files = @(Get-ChildItem -LiteralPath $directory -File -Recurse | Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE|PATENTS)([.\-_].*)?$' })
    if ($files.Count -eq 0) { throw "No license found for $modulePath" }
    $copied = @()
    foreach ($file in $files) {
      $relative = [IO.Path]::GetRelativePath($directory, $file.FullName)
      $target = Join-Path $destination $relative
      New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force | Out-Null
      Copy-Item -LiteralPath $file.FullName -Destination $target -Force
      $copied += $relative.Replace('\','/')
    }
    if ($fromVendor) {
      # Vendor omits unused upstream assets/testdata and their notices. Keep
      # the complete notices already supplied for this exact module version
      # when rebuilding an application source archive offline.
      $previous = @($previousInventory | Where-Object { $_.module -eq $modulePath -and $_.version -eq $version })
      foreach ($entry in $previous) {
        foreach ($relative in $entry.files) {
          $existing = [IO.Path]::GetFullPath((Join-Path $destination $relative))
          $prefix = [IO.Path]::GetFullPath($destination) + [IO.Path]::DirectorySeparatorChar
          if (-not $existing.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Invalid previous license path for ${modulePath}: $relative"
          }
          if (-not (Test-Path -LiteralPath $existing -PathType Leaf)) { throw "Packaged license is missing: $existing" }
          $copied += $relative.Replace('\','/')
        }
      }
    }
    $rows += [ordered]@{ module = $modulePath; version = $version; directory = "go/$id"; files = @($copied | Sort-Object -Unique) }
  }
  $goRoot = (go env GOROOT).Trim()
  if ($LASTEXITCODE -ne 0) { throw 'Cannot identify Go license' }
  $goLicense = Join-Path $Root 'licenses\go-toolchain\LICENSE'
  New-Item -ItemType Directory -Path (Split-Path -Parent $goLicense) -Force | Out-Null
  Copy-Item -LiteralPath (Join-Path $goRoot 'LICENSE') -Destination $goLicense -Force
  $rows | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $Root 'licenses\modules.json') -Encoding utf8
  Write-Output "Collected licenses for $($rows.Count) linked modules and the Go runtime."
} finally { Pop-Location }
