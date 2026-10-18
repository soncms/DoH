# Online, read-only source scan; never starts SonDNS or changes installed services.
param([string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path -Parent $PSScriptRoot
if (-not $OutputDirectory) {
  $OutputDirectory = Join-Path $root ('dist/security/' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [Guid]::NewGuid().ToString('N'))
}
$output = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $output) { throw 'Use a new scan output directory; prior scan results are never reused' }
New-Item -ItemType Directory -Path $output | Out-Null
$savedGoEnvironment = @{}
foreach ($name in @('GOFLAGS','GOWORK','GOTOOLCHAIN')) { $savedGoEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
Push-Location $root
try {
  # Scan this module's default application tags without modifying go.mod/go.sum
  # or inheriting another workspace or console-only GOFLAGS from the caller.
  $env:GOFLAGS = '-mod=readonly'
  $env:GOWORK = 'off'
  $env:GOTOOLCHAIN = 'local'
  $goOS = (go env GOOS).Trim()
  if ($LASTEXITCODE -ne 0) { throw 'Cannot identify Go target OS' }
  $goARCH = (go env GOARCH).Trim()
  if ($LASTEXITCODE -ne 0) { throw 'Cannot identify Go target architecture' }
  $goVersion = (go env GOVERSION).Trim()
  if ($LASTEXITCODE -ne 0) { throw 'Cannot identify Go toolchain' }
  if ($goOS -cne 'windows' -or $goARCH -cne 'amd64') { throw 'Scan the Windows amd64 application target' }
  $commit = ''
  $dirty = $true
  if (Test-Path -LiteralPath (Join-Path $root '.git')) {
    $commit = (git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Cannot identify source commit' }
    $status = @(git status --porcelain --untracked-files=normal)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot identify source status' }
    $dirty = $status.Count -gt 0
  }
  $metadata = [ordered]@{ sourceCommit = $commit; sourceDirty = $dirty; goOS = $goOS; goARCH = $goARCH
    goVersion = $goVersion; scannerModule = 'golang.org/x/vuln'; scannerVersion = 'v1.9.0'
    startedUTC = [DateTime]::UtcNow.ToString('o'); includesTests = $false; buildTags = @()
  }
  $metadata | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $output 'metadata.json') -Encoding utf8
  go list -m -json all > (Join-Path $output 'modules.json')
  if ($LASTEXITCODE -ne 0) { throw 'Cannot collect module/replacement provenance' }
  go list -deps -f '{{.ImportPath}}' ./cmd/sondns > (Join-Path $output 'packages.txt')
  if ($LASTEXITCODE -ne 0) { throw 'Cannot collect the application package graph' }
  # JSON output can exit zero even when findings exist. The independent parser
  # below must pass as well; do not replace it with an exit-code-only check.
  go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -json -scan=symbol ./cmd/sondns > (Join-Path $output 'govulncheck.json') 2> (Join-Path $output 'scanner.err')
  $scanExitCode = $LASTEXITCODE
  $metadata['scannerExitCode'] = $scanExitCode
  $metadata['completedUTC'] = [DateTime]::UtcNow.ToString('o')
  $metadata['rawOutputSHA256'] = (Get-FileHash -LiteralPath (Join-Path $output 'govulncheck.json') -Algorithm SHA256).Hash.ToLowerInvariant()
  $metadata | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $output 'metadata.json') -Encoding utf8
  if ($scanExitCode -ne 0) { throw "govulncheck failed; inspect $output" }
  python -I -B (Join-Path $PSScriptRoot 'go_vulnerability_report.py') --input (Join-Path $output 'govulncheck.json') --metadata (Join-Path $output 'metadata.json') --modules (Join-Path $output 'modules.json') --packages (Join-Path $output 'packages.txt') --baseline-evidence (Join-Path $output 'local-baselines.json') --query-baselines --output (Join-Path $output 'summary.json')
  if ($LASTEXITCODE -ne 0) { throw "Vulnerability gate failed; inspect $output" }
  Write-Output "Go vulnerability report: $output"
} finally {
  Pop-Location
  foreach ($name in $savedGoEnvironment.Keys) { [Environment]::SetEnvironmentVariable($name, $savedGoEnvironment[$name], 'Process') }
}
