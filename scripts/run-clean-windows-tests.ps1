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
foreach ($name in @('SonDNS.exe','SonDNS.tests.exe','run-clean-windows-tests.ps1','ipv6-test-fixture.ps1')) {
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
$ipv6StatePath = Join-Path $reportRoot 'ipv6-network.json'
try {
  foreach ($name in @('SONDNS_SERVICE_TEST','SONDNS_ACCOUNT_TEST','SONDNS_TEST_EXE','SONDNS_SERVICE_TEST_PARENT','SONDNS_SERVICE_TEST_LOG','SONDNS_TEST_IPV6_NETWORK')) { $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
  $env:SONDNS_SERVICE_TEST = '1'
  $env:SONDNS_ACCOUNT_TEST = '1'
  $env:SONDNS_TEST_EXE = Join-Path $KitDirectory 'SonDNS.exe'
  $env:SONDNS_SERVICE_TEST_PARENT = "$env:SystemDrive\"
  $env:SONDNS_SERVICE_TEST_LOG = Join-Path $reportRoot 'service-runtime.log'
  & (Join-Path $KitDirectory 'ipv6-test-fixture.ps1') -Mode Create -StateFile $ipv6StatePath -DisposableVM
  $env:SONDNS_TEST_IPV6_NETWORK = Join-Path $reportRoot 'ipv6-network-setup.json'
  Copy-Item -LiteralPath $ipv6StatePath -Destination $env:SONDNS_TEST_IPV6_NETWORK
  $report.testStarted = $true
  Save-Report
  $stdout = Join-Path $reportRoot 'service-test.log'
  $stderr = Join-Path $reportRoot 'service-test.err'
  $process = Start-Process -FilePath (Join-Path $KitDirectory 'SonDNS.tests.exe') -ArgumentList @('-test.v','-test.run=^TestWindowsServiceLifecycle$','-test.timeout=300s') -WorkingDirectory $KitDirectory -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
  if (-not $process.WaitForExit(320000)) { $process.Kill(); $process.WaitForExit(); throw 'Lifecycle test timed out; inspect surviving service/driver, then restore the VM snapshot or discard the hosted VM' }
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
    'live migration rollback kept candidate image locked through old-service DNS recovery',
    'live migration completed: custom config, SCM fields, tray, backup hashes and DNS verified',
    'live migration cleanup verified: service, driver, tray and both helper paths absent',
    'live GUI migration cancellation: default No, exact state and DNS unchanged',
    'live GUI migration validation failure: actual file lock, no backup, exact state and DNS unchanged',
    'live GUI migration success: confirmation, imported controls and concrete backup path verified',
    'live interrupted migration verified: prepared;', 'live interrupted migration verified: switching;',
    'live interrupted migration verified: after-stop;', 'live interrupted migration verified: bound;',
    'live interrupted migration verified: after-start;', 'live interrupted migration verified: after-tray;',
    'cross-account access verified: standard monitoring allowed; config, SCM and IPC mutations denied',
    'cross-account administrator verified: protected access and authorized IPC log clear',
    'cross-account handoff wait verified: separate administrator waited for the live standard-user process',
    'temporary Windows accounts removed and owned child exited',
    'IPv6 fixture before service: documentation and scoped link-local A/AAAA timed out',
    'transparent IPv6 documentation and scoped link-local A/AAAA replies verified',
    'IPv6 fixture after service: documentation and scoped link-local A/AAAA timed out')
  foreach ($check in $checks) {
    if (-not $log.Contains($check)) { throw "Lifecycle evidence is missing: $check" }
  }
  $migration = Get-Content -LiteralPath (Join-Path $reportRoot 'migration.json') -Raw | ConvertFrom-Json
  $points = @('prepared','switching','after-stop','bound','after-start','after-tray')
  if (@($migration.Interruptions).Count -ne $points.Count) { throw 'Migration interruption cases are incomplete' }
  foreach ($point in $points) {
    $items = @($migration.Interruptions | Where-Object { $_.Point -ceq $point })
    if ($items.Count -ne 1) { throw "Missing/duplicate migration interruption point: $point" }
    $item = $items[0]
    $expectedStatus = if ($point -in @('prepared','switching')) { $point } elseif ($point -ceq 'after-stop') { 'switching' } else { 'bound' }
    $expectedState = if ($point -in @('after-stop','bound')) { 1 } else { 4 }
    $expectedBinding = if ($point -in @('bound','after-start','after-tray')) { $migration.AfterMigration.BinaryPathName } else { $migration.Before.BinaryPathName }
    $expectedTray = if ($point -ceq 'after-tray') { $migration.TrayAfterMigration } else { $migration.TrayBefore }
    if ($item.Stage -cne 'completed' -or $item.ChildPID -le 0 -or $item.ChildExitCode -ne 173 -or $item.ChildTerminated -ne $true -or
        $item.GUIUnchanged -ne $true -or $item.ManualRestoreVerified -ne $true -or $item.GUIProcessesReaped -ne 1 -or
        $item.JournalStatus -cne $expectedStatus -or $item.ServiceState -ne $expectedState -or
        $item.Binding.BinaryPathName -cne $expectedBinding -or $item.Tray.Kind -ne $expectedTray.Kind -or
        $item.Tray.Exists -ne $expectedTray.Exists -or $item.Tray.Value -cne $expectedTray.Value -or
        $item.DNSAvailable -ne ($expectedState -eq 4) -or $item.DNSUnavailable -ne ($expectedState -eq 1) -or
        $item.Backup.SchemaVersion -ne 1 -or $item.Backup.Recovery.Ready -ne $true -or
        $item.Backup.Recovery.DestinationExisted -ne $true -or
        $item.Backup.Recovery.DestinationSHA256 -cne $migration.DestinationConfigBeforeSHA256 -or
        $item.Backup.Recovery.CandidateSHA256 -cne $item.DestinationSHA256 -or
        $item.Backup.Recovery.FilesManifestSHA256 -cne $item.Backup.ManifestSHA256 -or
        $item.JournalSHA256 -cnotmatch '^[a-f0-9]{64}$') { throw "Incomplete interruption/GUI/manual-recovery evidence: $point" }
  }
  if (-not (Test-Path -LiteralPath $env:SONDNS_SERVICE_TEST_LOG -PathType Leaf)) { throw 'Service runtime log is missing' }
  if ($migration.RollbackVerified -ne $true -or $migration.MigrationVerified -ne $true -or
      $migration.CleanupVerified -ne $true -or $migration.SCMStartErrorCode -le 0 -or
      $migration.CandidateValidationErrorCode -ne 32 -or -not $migration.CandidateValidationError -or
      $migration.SourceRestartValidationPassed -ne $true -or $migration.CandidateLockHeldDuringRestart -ne $true -or
      $migration.DNSRecoveredWithCandidateLocked -ne $true -or $migration.CandidateLockReleased -ne $true -or
      $migration.RollbackBackup.Status -cne 'rolled-back' -or $migration.CompletedBackup.Status -cne 'complete' -or
      $migration.RollbackBackup.RuntimeFiles -lt 1 -or $migration.CompletedBackup.RuntimeFiles -lt 1 -or
      $migration.GUI.CancelledUnchanged -ne $true -or $migration.GUI.ValidationFailureUnchanged -ne $true -or
      $migration.GUI.Completed -ne $true -or $migration.GUI.DefaultButton -ne 7 -or
      $migration.GUI.FailureButtonID -notin @(1,2) -or
      $migration.GUI.ProcessesReaped -ne 2) {
    throw 'The live migration/rollback report is incomplete'
  }
  $ipv6 = Get-Content -LiteralPath (Join-Path $reportRoot 'ipv6-dns.json') -Raw | ConvertFrom-Json
  if (@($ipv6.Before).Count -ne 4 -or @($ipv6.Intercepted).Count -ne 4 -or @($ipv6.After).Count -ne 4 -or
      @(@($ipv6.Before) + @($ipv6.After) | Where-Object { $_.TimedOut -ne $true -or $_.SyntheticReply -ne $false }).Count -or
      @($ipv6.Intercepted | Where-Object { $_.SyntheticReply -ne $true -or $_.TimedOut -ne $false }).Count) {
    throw 'The transparent IPv6 before/running/after report is incomplete'
  }
  $accounts = Get-Content -LiteralPath (Join-Path $reportRoot 'accounts.json') -Raw | ConvertFrom-Json
  if ($accounts.StandardVerified -ne $true -or $accounts.AdministratorVerified -ne $true -or
      $accounts.HandoffVerified -ne $true -or $accounts.ChildExited -ne $true -or $accounts.AccountsRemoved -ne $true -or
      $accounts.ChildForced -ne $false -or $accounts.ChildExitCode -ne 0 -or
      $accounts.Standard.Elevated -ne $false -or $accounts.Standard.Administrator -ne $false -or
      $accounts.Administrator.Elevated -ne $true -or $accounts.Administrator.Administrator -ne $true -or
      $accounts.Standard.ConfigWriteDenied -ne 5 -or $accounts.Standard.SCMMutateDenied -ne 5 -or
      $accounts.Standard.PipeCreateDenied -ne 5 -or @($accounts.Standard.RejectedOperations).Count -ne 2 -or
      $accounts.Standard.Removed -ne $true -or $accounts.Administrator.Removed -ne $true -or
      $accounts.Administrator.LogClearAllowed -ne $true -or $accounts.HandoffError -or
      $accounts.HandoffBeforeDenied -ne 5 -or $accounts.HandoffAccess.RestoreVerified -ne $true -or
      $accounts.HandoffAccess.Failed -ne $false -or $accounts.HandoffAccess.Stage -cne 'completed' -or
      $accounts.HandoffAccess.Error -or $accounts.HandoffAccess.NativeError -ne 0 -or
      $accounts.HandoffAccess.SessionID -ne $accounts.ChildSessionID -or $accounts.HandoffWait -le 0 -or
      -not $accounts.HandoffAccess.Before -or $accounts.HandoffAccess.Before -cne $accounts.HandoffAccess.Restored -or
      $accounts.HandoffAccess.Before -ceq $accounts.HandoffAccess.Granted -or
      $accounts.HandoffRendezvous -cne 'duplicated-anonymous-events' -or $accounts.HandoffEventCount -ne 4 -or
      $accounts.HandoffEventAccess -ne 0x100002 -or $accounts.HandoffAccess.EventHandlesVerified -ne 4 -or
      -not $accounts.HandoffUnsafeFixtureError -or $accounts.HandoffFixtureRemoved -ne $true -or
      $accounts.ServiceFolderRevalidated -ne $true -or
      $accounts.HandoffControlDenied.terminate -ne 5 -or $accounts.HandoffControlDenied.writeMemory -ne 5 -or
      $accounts.HandoffControlDenied.changeDACL -ne 5 -or
      $accounts.ChildSID -cne $accounts.Standard.SID -or $accounts.CreatorSID -ceq $accounts.Standard.SID -or
      $accounts.CreatorSID -ceq $accounts.Administrator.SID -or $accounts.Standard.SID -ceq $accounts.Administrator.SID) {
    throw 'The real cross-account access/handoff/cleanup report is incomplete'
  }
  $report['checksPassed'] = $checks
  $report.lifecyclePassed = $true
} catch {
  $failure = $_
  $report['error'] = $_.Exception.Message
} finally {
  if (Test-Path -LiteralPath $ipv6StatePath) {
    try {
      & (Join-Path $KitDirectory 'ipv6-test-fixture.ps1') -Mode Remove -StateFile $ipv6StatePath -DisposableVM
      $network = Get-Content -LiteralPath $ipv6StatePath -Raw | ConvertFrom-Json
      if ($network.cleanupVerified -ne $true) { throw 'Synthetic IPv6 cleanup is unverified' }
      $report['ipv6NetworkCleanupVerified'] = $true
    } catch {
      $report.lifecyclePassed = $false
      if ($null -eq $failure) { $failure = $_ }
      $report['error'] = $_.Exception.Message
    }
  }
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
