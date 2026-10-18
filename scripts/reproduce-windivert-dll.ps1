param(
  [Parameter(Mandatory)][string]$InputDll,
  [Parameter(Mandatory)][string]$OutputDll
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$inputPath = [IO.Path]::GetFullPath($InputDll)
$outputPath = [IO.Path]::GetFullPath($OutputDll)
if (Test-Path -LiteralPath $outputPath) { throw "Output already exists: $outputPath" }
$data = [IO.File]::ReadAllBytes($inputPath)
function Get-ByteHash([byte[]]$Bytes) {
  $hasher = [Security.Cryptography.SHA256]::Create()
  try { return [Convert]::ToHexString($hasher.ComputeHash($Bytes)).ToLowerInvariant() }
  finally { $hasher.Dispose() }
}
$expectedInput = 'c1e060ee19444a259b2162f8af0f3fe8c4428a1c6f694dce20de194ac8d7d9a2'
$expectedOutput = '16abd6a029e65557c6a309bea7b13bf81fff4e193567582e1cddbf6719f323e0'
if ((Get-ByteHash $data) -cne $expectedInput) { throw 'Input is not the pinned upstream WinDivert 2.2.2-A x64 DLL' }
$pe = [BitConverter]::ToInt32($data, 0x3c)
$optional = $pe + 24
if ([BitConverter]::ToUInt32($data, $pe) -ne 0x4550 -or
  [BitConverter]::ToUInt16($data, $pe + 4) -ne 0x8664 -or
  [BitConverter]::ToUInt16($data, $optional) -ne 0x20b) { throw 'Unexpected PE64 layout' }

# The bundled file adds IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE (0x40).
# No executable section, import/export table or relocation is changed.
$flagsOffset = $optional + 70
$flags = [BitConverter]::ToUInt16($data, $flagsOffset) -bor 0x40
[BitConverter]::GetBytes([uint16]$flags).CopyTo($data, $flagsOffset)
$checksumOffset = $optional + 64
[BitConverter]::GetBytes([uint32]0).CopyTo($data, $checksumOffset)
[uint64]$sum = 0
for ($i = 0; $i -lt $data.Length; $i += 2) {
  [uint32]$word = $data[$i]
  if ($i + 1 -lt $data.Length) { $word = $word -bor ([uint32]$data[$i + 1] -shl 8) }
  $sum += $word
  $sum = ($sum -band 0xffff) + ($sum -shr 16)
}
$sum = ($sum -band 0xffff) + ($sum -shr 16)
$checksum = [uint32](($sum -band 0xffff) + $data.Length)
[BitConverter]::GetBytes($checksum).CopyTo($data, $checksumOffset)
if ((Get-ByteHash $data) -cne $expectedOutput) { throw 'Header patch did not reproduce the bundled DLL hash' }

# CreateNew refuses an existing output even if it appeared after the check.
$stream = [IO.FileStream]::new($outputPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
try { $stream.Write($data, 0, $data.Length); $stream.Flush($true) }
finally { $stream.Dispose() }
Write-Output "Reproduced bundled WinDivert.dll: $expectedOutput"
