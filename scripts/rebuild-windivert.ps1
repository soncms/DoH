param(
  [Parameter(Mandatory)][string]$SourceArchive,
  [Parameter(Mandatory)][string]$OutputDirectory,
  [string]$Root = (Split-Path -Parent $PSScriptRoot)
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$lock = Get-Content -LiteralPath (Join-Path $Root 'assets/native-source-lock.json') -Raw | ConvertFrom-Json
$source = @($lock.archives | Where-Object { $_.component -eq 'windivert' -and $_.kind -eq 'source' })
if ($source.Count -ne 1) { throw 'Expected one pinned WinDivert source archive' }
if ((Get-FileHash -LiteralPath $SourceArchive -Algorithm SHA256).Hash.ToLowerInvariant() -cne $source[0].sha256) { throw 'WinDivert source archive hash mismatch' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw "Build output already exists: $OutputDirectory" }
$compiler = Get-Command gcc.exe -ErrorAction Stop
$strip = Get-Command strip.exe -ErrorAction Stop
$target = (& $compiler.Source -dumpmachine).Trim()
if ($LASTEXITCODE -ne 0 -or $target -notmatch '^x86_64-(?:.*mingw32|.*windows-gnu)$') { throw 'An x86_64 MinGW-w64 compatible GNU-target GCC/Clang toolchain is required' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$extracted = Join-Path $OutputDirectory 'source'
[IO.Compression.ZipFile]::ExtractToDirectory([IO.Path]::GetFullPath($SourceArchive), $extracted)
$tree = Join-Path $extracted "WinDivert-$($lock.windivert.sourceCommit)"
$dll = Join-Path $OutputDirectory 'WinDivert.dll'
$object = Join-Path $OutputDirectory 'windivert.o'
Push-Location $tree
try {
  # The upstream mingw-build.sh DLL commands, restricted to amd64. The
  # dynamic-base flag is explicit; no driver is built, installed or started.
  # Modern MinGW/LLVM headers no longer supply offsetof transitively.
  $compileFlags = @('-fno-ident','-Wall','-Wno-pointer-to-int-cast','-Os','-Iinclude/','-include','stddef.h')
  $linkFlags = @('-fno-ident','-shared','-Wl,--enable-stdcall-fixup','-Wl,--entry=WinDivertDllEntry','-Wl,--dynamicbase')
  & $compiler.Source @compileFlags -c dll/windivert.c -o $object
  if ($LASTEXITCODE -ne 0) { throw 'WinDivert DLL compilation failed' }
  # Clang may emit memcpy for aggregate copies. MSVCRT supplies that standard
  # Windows runtime primitive without requiring a DLL CRT startup entrypoint.
  & $compiler.Source @linkFlags -o $dll $object dll/windivert.def -nostdlib -lkernel32 -ladvapi32 -lmsvcrt
  if ($LASTEXITCODE -ne 0) { throw 'WinDivert DLL linking failed' }
  & $strip.Source $dll
  if ($LASTEXITCODE -ne 0) { throw 'WinDivert DLL symbol stripping failed' }
} finally { Pop-Location }
$compilerVersion = @(& $compiler.Source --version)[0]
if ($LASTEXITCODE -ne 0) { throw 'Could not identify compiler version' }
[ordered]@{
  component = 'WinDivert DLL'
  sourceCommit = $lock.windivert.sourceCommit
  sourceArchiveSha256 = $source[0].sha256
  compiler = $compilerVersion
  compilerTarget = $target
  sha256 = (Get-FileHash -LiteralPath $dll -Algorithm SHA256).Hash.ToLowerInvariant()
  dynamicBase = $true
  driverBuilt = $false
  note = 'A functional source rebuild, not a claim of byte identity with the upstream release. Review and test before replacing embedded assets.'
} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $OutputDirectory 'build-info.json') -Encoding utf8
Write-Output "Rebuilt WinDivert DLL: $dll"
