# Run only in a disposable Windows VM: restore a clean snapshot afterward, or
# discard a GitHub-hosted ephemeral VM at job end. Check mode is read-only
# apart from its report folder; Run mode installs/removes a service and driver.
param(
  [ValidateSet('Check','Run')][string]$Mode = 'Check',
  [switch]$DisposableVM,
  [string]$KitDirectory = $PSScriptRoot
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$KitDirectory = [IO.Path]::GetFullPath($KitDirectory)
$manifestPath = Join-Path $KitDirectory 'test-kit.json'
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
foreach ($name in @('SonDNS.exe','SonDNS.tests.exe','run-clean-windows-tests.ps1')) {
  $file = Join-Path $KitDirectory $name
  $item = Get-Item -LiteralPath $file
  if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Test kit link rejected: $name" }
  $expected = @($manifest.files | Where-Object { $_.path -eq $name })
  if ($expected.Count -ne 1 -or (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected[0].sha256) {
    throw "Test kit hash mismatch: $name"
  }
}
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$os = Get-CimInstance Win32_OperatingSystem
$machine = Get-CimInstance Win32_ComputerSystem
$before = @(Get-CimInstance Win32_Service -Filter "Name='SonDNS'" | Select-Object Name,State,StartMode,PathName)
$drivers = @(Get-CimInstance Win32_SystemDriver -Filter "Name='WinDivert'" | Select-Object Name,State,StartMode,PathName)
$processes = @(Get-CimInstance Win32_Process -Filter "Name='SonDNS.exe'" | Select-Object ProcessId,ExecutablePath)
$base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::LocalMachine,[Microsoft.Win32.RegistryView]::Registry64)
try {
  $run = $base.OpenSubKey('Software\Microsoft\Windows\CurrentVersion\Run')
  try { $trayExists = $null -ne $run -and @($run.GetValueNames()) -contains 'SonDNS' }
  finally { if ($null -ne $run) { $run.Dispose() } }
} finally { $base.Dispose() }
$reasons = [Collections.Generic.List[string]]::new()
if (-not $admin) { $reasons.Add('Run an elevated PowerShell in the disposable VM') }
if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') { $reasons.Add('Windows amd64 is required') }
if ($before.Count) { $reasons.Add('SonDNS service already exists; it will not be replaced') }
if ($drivers.Count) { $reasons.Add('WinDivert driver already exists; it will not be replaced') }
if ($processes.Count) { $reasons.Add('A SonDNS process is already running') }
if ($trayExists) { $reasons.Add('An existing SonDNS machine-wide Run value must be reviewed first') }
$reportRoot = Join-Path $KitDirectory ('reports\' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $reportRoot | Out-Null
$report = [ordered]@{
  mode = $Mode; version = $manifest.version; sourceManifestSha256 = $manifest.sourceManifestSha256
  operatorConfirmedDisposableVM = [bool]$DisposableVM; windows = $os.Caption; windowsBuild = $os.BuildNumber
  hypervisorPresent = $machine.HypervisorPresent; elevated = $admin; eligible = $reasons.Count -eq 0
  preflightReasons = @($reasons); serviceBefore = $before; driverBefore = $drivers
  signature = (Get-AuthenticodeSignature -LiteralPath (Join-Path $KitDirectory 'SonDNS.exe')).Status.ToString()
  testStarted = $false; lifecyclePassed = $false; fullReleaseValidationPassed = $false
}
$reportPath = Join-Path $reportRoot 'result.json'
function Save-Report { $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding UTF8 }
Save-Report
if ($Mode -eq 'Check') { $report | ConvertTo-Json -Depth 8; Write-Output "Preflight report: $reportPath"; return }
if (-not $DisposableVM) { throw 'Run mode requires -DisposableVM on a clean VM with a restore snapshot, or a GitHub-hosted ephemeral VM discarded at job end. HypervisorPresent alone does not prove isolation.' }
if ($reasons.Count) { throw ($reasons -join '; ') }
$savedEnv = @{}
$failure = $null
try {
  foreach ($name in @('SONDNS_SERVICE_TEST','SONDNS_TEST_EXE','SONDNS_SERVICE_TEST_PARENT','SONDNS_SERVICE_TEST_LOG')) { $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
  $env:SONDNS_SERVICE_TEST = '1'
  $env:SONDNS_TEST_EXE = Join-Path $KitDirectory 'SonDNS.exe'
  $env:SONDNS_SERVICE_TEST_PARENT = "$env:SystemDrive\"
  $env:SONDNS_SERVICE_TEST_LOG = Join-Path $reportRoot 'service-runtime.log'
  $report.testStarted = $true
  Save-Report
  $stdout = Join-Path $reportRoot 'service-test.log'
  $stderr = Join-Path $reportRoot 'service-test.err'
  $process = Start-Process -FilePath (Join-Path $KitDirectory 'SonDNS.tests.exe') -ArgumentList @('-test.v','-test.run=^TestWindowsServiceLifecycle$','-test.timeout=180s') -WorkingDirectory $KitDirectory -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
  if (-not $process.WaitForExit(200000)) { $process.Kill(); $process.WaitForExit(); throw 'Lifecycle test timed out; inspect surviving service/driver, then restore the VM snapshot or discard the hosted VM' }
  $process.Refresh()
  $report['testExitCode'] = $process.ExitCode
  $log = Get-Content -LiteralPath $stdout -Raw
  if ($process.ExitCode -ne 0 -or $log -notmatch '(?m)^--- PASS: TestWindowsServiceLifecycle ' -or $log -match '(?m)^--- SKIP: TestWindowsServiceLifecycle ') {
    throw "Lifecycle test did not pass. Read $stdout and $stderr"
  }
  $checks = @('in-flight intercepted query held upstream during stop',
    'embedded DPI helper restarted after forced exit', 'embedded DPI helper stopped without orphan',
    'quiet stop completed (OS background traffic not excluded)', 'runtime log: no shutdown/close/cleanup errors',
    'live migration rollback after real SCM startup failure',
    'live migration completed: custom config, SCM fields, tray, backup hashes and DNS verified',
    'live migration cleanup verified: service, driver, tray and both helper paths absent')
  foreach ($check in $checks) {
    if (-not $log.Contains($check)) { throw "Lifecycle evidence is missing: $check" }
  }
  if (-not (Test-Path -LiteralPath $env:SONDNS_SERVICE_TEST_LOG -PathType Leaf)) { throw 'Service runtime log is missing' }
  $migration = Get-Content -LiteralPath (Join-Path $reportRoot 'migration.json') -Raw | ConvertFrom-Json
  if ($migration.RollbackVerified -ne $true -or $migration.MigrationVerified -ne $true -or
      $migration.CleanupVerified -ne $true -or $migration.SCMStartErrorCode -le 0 -or
      $migration.RollbackBackup.Status -cne 'rolled-back' -or $migration.CompletedBackup.Status -cne 'complete' -or
      $migration.RollbackBackup.RuntimeFiles -lt 1 -or $migration.CompletedBackup.RuntimeFiles -lt 1) {
    throw 'The live migration/rollback report is incomplete'
  }
  $report['checksPassed'] = $checks
  $report.lifecyclePassed = $true
} catch {
  $failure = $_
  $report['error'] = $_.Exception.Message
} finally {
  foreach ($name in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($name,$savedEnv[$name],'Process') }
  $report['serviceAfter'] = @(Get-CimInstance Win32_Service -Filter "Name='SonDNS'" | Select-Object Name,State,StartMode,PathName)
  $report['driverAfter'] = @(Get-CimInstance Win32_SystemDriver -Filter "Name='WinDivert'" | Select-Object Name,State,StartMode,PathName)
  if ($report.serviceAfter.Count -or $report.driverAfter.Count) {
    $report.lifecyclePassed = $false
    if ($null -eq $failure) { $failure = 'Service/driver registration remains; inspect the report, then restore the VM snapshot or discard the hosted VM' }
    $report['error'] = [string]$failure
  }
  Save-Report
}
Write-Output "Lifecycle report: $reportPath"
if ($null -ne $failure) { throw $failure }
