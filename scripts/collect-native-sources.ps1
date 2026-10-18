param(
  [string]$Root = (Split-Path -Parent $PSScriptRoot),
  [Parameter(Mandatory)][string]$OutputDirectory,
  [string]$CacheDirectory,
  [string]$PythonCommand = 'python'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$lockPath = Join-Path $Root 'assets/native-source-lock.json'
$lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json
if ($lock.schemaVersion -ne 1 -or @($lock.archives).Count -eq 0) { throw 'Unsupported/empty native source lock' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw "Native source output already exists: $OutputDirectory" }
$names = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
$historicalCygwin = 'http://ctm.crouchingtigerhiddenfruitbat.org/pub/cygwin/circa/64bit/2024/01/31/234856/x86_64/release/cygwin/'
foreach ($entry in $lock.archives) {
  # Only the two exact historical Cygwin URLs may use HTTP. Their pinned
  # hashes and independent source/blob correspondence are checked below.
  $historical = $entry.component -ceq 'cygwin' -and
    $entry.file -cin @('cygwin-3.4.10-1.tar.xz', 'cygwin-3.4.10-1-src.tar.xz') -and
    $entry.url -ceq ($historicalCygwin + $entry.file)
  if ($entry.file -cnotmatch '^[a-zA-Z0-9][a-zA-Z0-9._-]*\.(zip|tar\.xz)$' -or -not $names.Add($entry.file) -or
    $entry.sha256 -cnotmatch '^[0-9a-f]{64}$' -or (([uri]$entry.url).Scheme -cne 'https' -and -not $historical)) {
    throw 'Invalid native source archive lock entry'
  }
}
& $PythonCommand -I -B -c 'import sys, tarfile; assert sys.version_info >= (3, 10), "Python 3.10+ is required for native source verification"'
if ($LASTEXITCODE -ne 0) { throw 'Python 3.10+ is required for source packaging (not for the portable application)' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
foreach ($entry in $lock.archives) {
  $target = Join-Path $OutputDirectory $entry.file
  $cache = if ($CacheDirectory) { Join-Path $CacheDirectory $entry.file } else { '' }
  if ($cache -and (Test-Path -LiteralPath $cache -PathType Leaf)) {
    if ((Get-FileHash -LiteralPath $cache -Algorithm SHA256).Hash.ToLowerInvariant() -cne $entry.sha256) {
      throw "Native source cache hash mismatch: $($entry.file)"
    }
    Copy-Item -LiteralPath $cache -Destination $target
  } else {
    Invoke-WebRequest -Uri $entry.url -OutFile $target -TimeoutSec 60
  }
  if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant() -cne $entry.sha256) {
    throw "Native source download hash mismatch: $($entry.file)"
  }
}

# Reproduce the shipped DLL from the exact upstream reference bytes, and
# compare the driver against the same archive without loading either file.
$reference = @($lock.archives | Where-Object { $_.component -eq 'windivert' -and $_.kind -eq 'reference-binary' })
if ($reference.Count -ne 1) { throw 'Expected one WinDivert reference archive' }
$zip = [IO.Compression.ZipFile]::OpenRead((Join-Path $OutputDirectory $reference[0].file))
$temporary = Join-Path $OutputDirectory ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
$inputDll = Join-Path $temporary 'upstream.dll'
$patchedDll = Join-Path $temporary 'patched.dll'
try {
  foreach ($memberName in @($lock.windivert.referenceDllMember, $lock.windivert.driverMember)) {
    $member = $zip.GetEntry($memberName)
    if ($null -eq $member) { throw "Missing upstream archive member: $memberName" }
    $stream = $member.Open()
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { $hash = [Convert]::ToHexString($hasher.ComputeHash($stream)).ToLowerInvariant() }
    finally { $stream.Dispose(); $hasher.Dispose() }
    $want = if ($memberName -eq $lock.windivert.referenceDllMember) { $lock.windivert.referenceDllSha256 } else { $lock.windivert.driverSha256 }
    if ($hash -cne $want) { throw "Upstream member hash mismatch: $memberName" }
  }
  [IO.Compression.ZipFileExtensions]::ExtractToFile($zip.GetEntry($lock.windivert.referenceDllMember), $inputDll)
  & (Join-Path $Root $lock.windivert.patch) -InputDll $inputDll -OutputDll $patchedDll
  if (-not $?) { throw 'WinDivert header reproduction failed' }
  $pins = Get-Content -LiteralPath (Join-Path $Root 'assets/runtime-sha256.json') -Raw | ConvertFrom-Json
  if ((Get-FileHash -LiteralPath $patchedDll -Algorithm SHA256).Hash.ToLowerInvariant() -cne $pins.assets.'third_party/zapret2/WinDivert.dll' -or
    $lock.windivert.driverSha256 -cne $pins.assets.'third_party/zapret2/WinDivert64.sys') {
    throw 'Native source evidence does not match the shipped runtime pins'
  }
} finally {
  $zip.Dispose()
  # Delete only our two known temporary files and then the empty directory.
  foreach ($path in @($inputDll, $patchedDll)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path } }
  Remove-Item -LiteralPath $temporary
}
& $PythonCommand -I -B (Join-Path $Root 'scripts/verify_cygwin_source.py') --root $Root --archives $OutputDirectory
if ($LASTEXITCODE -ne 0) { throw 'Cygwin binary/source correspondence verification failed' }
# Write the collection receipt only after all archives and member evidence pass.
Copy-Item -LiteralPath $lockPath -Destination (Join-Path $OutputDirectory 'manifest.json')
Write-Output "Collected $($lock.archives.Count) pinned native source/reference archives. Status: $($lock.status)"
