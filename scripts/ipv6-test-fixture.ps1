# Test infrastructure only. Add exact synthetic IPv6 resources on a disposable
# VM; never change adapter DNS, IPv4 configuration, default routes or bindings.
param(
  [Parameter(Mandatory)][ValidateSet('Create','Remove')][string]$Mode,
  [Parameter(Mandatory)][string]$StateFile,
  [switch]$DisposableVM
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $DisposableVM) { throw 'IPv6 fixture requires an explicitly disposable Windows VM' }
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin -or $env:PROCESSOR_ARCHITECTURE -ne 'AMD64') { throw 'IPv6 fixture requires an elevated Windows amd64 process' }
$StateFile = [IO.Path]::GetFullPath($StateFile)

function Save-Fixture {
  $json = $script:state | ConvertTo-Json -Depth 8
  [IO.File]::WriteAllText($StateFile, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}
function Get-FixtureAddresses {
  @(Get-NetIPAddress -AddressFamily IPv6 -InterfaceIndex $script:state.interfaceIndex |
    Where-Object { $_.IPAddress -eq $script:state.source })
}
function Get-FixtureRoutes {
  @(Get-NetRoute -AddressFamily IPv6 -InterfaceIndex $script:state.interfaceIndex |
    Where-Object { $_.DestinationPrefix -eq "$($script:state.destination)/128" })
}
function Get-FixtureSourceRoutes {
  @(Get-NetRoute -AddressFamily IPv6 -InterfaceIndex $script:state.interfaceIndex |
    Where-Object { $_.DestinationPrefix -eq "$($script:state.source)/128" })
}
function Get-FixtureNeighbors([string]$Address) {
  @(Get-NetNeighbor -AddressFamily IPv6 -PolicyStore ActiveStore -InterfaceIndex $script:state.interfaceIndex |
    Where-Object { $_.IPAddress -eq $Address })
}
function Get-FixtureInterface {
  Get-NetIPInterface -AddressFamily IPv6 -InterfaceIndex $script:state.interfaceIndex |
    Select-Object Dhcp,RouterDiscovery,AutomaticMetric,InterfaceMetric,NlMtu,ConnectionState
}
function Assert-FixtureIdentity {
  if ($script:state.schemaVersion -ne 1 -or $script:state.owner -notmatch '^[a-f0-9]{32}$' -or
      $script:state.machine -cne $env:COMPUTERNAME) { throw 'IPv6 fixture state does not belong to this VM' }
  $segment = $script:state.owner.Substring(0,4)
  if ($script:state.source -cne ([Net.IPAddress]::Parse("2001:db8:5344:${segment}::10").ToString()) -or
      $script:state.destination -cne ([Net.IPAddress]::Parse("2001:db8:5344:${segment}::53").ToString()) -or
      $script:state.linkLocalDestination -cne ([Net.IPAddress]::Parse("fe80::5344:${segment}:53").ToString()) -or
      $script:state.neighborMAC -cne ('02-53-' + ($script:state.owner.Substring(4,8) -replace '(..)(..)(..)(..)','$1-$2-$3-$4'))) {
    throw 'IPv6 fixture resource scope is invalid'
  }
  $adapter = Get-NetAdapter -InterfaceIndex $script:state.interfaceIndex
  if ($adapter.InterfaceGuid.ToString() -cne $script:state.interfaceGuid) { throw 'IPv6 fixture adapter identity changed' }
}
function Assert-InterfaceUnchanged {
  $script:state.interfaceAfter = Get-FixtureInterface
  Save-Fixture
  $current = $script:state.interfaceAfter | ConvertTo-Json -Compress
  if ($current -cne ($script:state.interfaceBefore | ConvertTo-Json -Compress)) {
    throw 'IPv6 interface policy changed; retain evidence and discard/restore the disposable VM'
  }
}
function Remove-Fixture {
  Assert-FixtureIdentity
  # Remove the synthetic route/source before neighbors so no queued traffic
  # from that source can recreate destination cache entries during cleanup.
  if ($script:state.ownsRoute) {
    foreach ($route in @(Get-FixtureRoutes)) {
      if ($route.NextHop -ne '::' -or $route.RouteMetric -ne 1) { throw 'IPv6 route changed concurrently; cleanup preserved it' }
      $route | Remove-NetRoute -Confirm:$false
    }
    $script:state.ownsRoute = $false
    Save-Fixture
  }
  if ($script:state.ownsSource) {
    foreach ($address in @(Get-FixtureAddresses)) {
      if ($address.PrefixLength -ne 128 -or -not $address.SkipAsSource) { throw 'IPv6 source does not match the owned /128 SkipAsSource entry; cleanup preserved it' }
      $address | Remove-NetIPAddress -Confirm:$false
    }
    $script:state.ownsSource = $false
    Save-Fixture
  }
  foreach ($entry in @(
    @{ address=$script:state.destination; owned='ownsDestinationNeighbor' },
    @{ address=$script:state.linkLocalDestination; owned='ownsLinkLocalNeighbor' }
  )) {
    if ($script:state.($entry.owned)) {
      $neighbors = @(Get-FixtureNeighbors $entry.address)
      $observation = [ordered]@{ address=$entry.address; before=@($neighbors | Select-Object IPAddress,LinkLayerAddress,State); after=@(); removed=$false }
      $script:state.neighborRemoval += $observation
      Save-Fixture
      foreach ($neighbor in $neighbors) {
        if ($neighbor.LinkLayerAddress -ine $script:state.neighborMAC -or $neighbor.State -ne 'Permanent') {
          throw 'IPv6 neighbor changed concurrently; cleanup did not overwrite it'
        }
        # Use an explicit store/key instead of CIM InputObject deletion. Keep
        # ownership until a fresh query confirms disappearance, not just a
        # successful command return.
        Remove-NetNeighbor -AddressFamily IPv6 -InterfaceIndex $script:state.interfaceIndex -IPAddress $entry.address -LinkLayerAddress $script:state.neighborMAC -State Permanent -PolicyStore ActiveStore -Confirm:$false
      }
      $deadline = [DateTime]::UtcNow.AddSeconds(5)
      do {
        $remaining = @(Get-FixtureNeighbors $entry.address)
        if (-not $remaining.Count) { break }
        Start-Sleep -Milliseconds 200
      } while ([DateTime]::UtcNow -lt $deadline)
      $observation.after = @($remaining | Select-Object IPAddress,LinkLayerAddress,State)
      Save-Fixture
      if ($remaining.Count) { throw "IPv6 neighbor removal not confirmed: $($observation.after | ConvertTo-Json -Compress)" }
      $observation.removed = $true
      $script:state.($entry.owned) = $false
      Save-Fixture
    }
  }
  $script:state.resourcesAfterRemoval = [ordered]@{
    sourceAddresses=@(Get-FixtureAddresses).Count; destinationRoutes=@(Get-FixtureRoutes).Count
    sourceRoutes=@(Get-FixtureSourceRoutes).Count
    destinationNeighbors=@(Get-FixtureNeighbors $script:state.destination).Count
    linkLocalNeighbors=@(Get-FixtureNeighbors $script:state.linkLocalDestination).Count
  }
  Save-Fixture
  if (@($script:state.resourcesAfterRemoval.Values | Where-Object { $_ -ne 0 }).Count) {
    throw 'Synthetic IPv6 resources remain; discard/restore the disposable VM'
  }
  Assert-InterfaceUnchanged
  $script:state.cleanupVerified = $true
  Save-Fixture
}

if ($Mode -eq 'Remove') {
  if ((Get-Item -LiteralPath $StateFile).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'IPv6 state link rejected' }
  $script:state = Get-Content -LiteralPath $StateFile -Raw | ConvertFrom-Json
  Remove-Fixture
  return
}
if (Test-Path -LiteralPath $StateFile) { throw 'IPv6 fixture state already exists; never replace another fixture' }
$candidates = @(Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' | Sort-Object RouteMetric)
$selected = $null
foreach ($candidate in $candidates) {
  $adapter = Get-NetAdapter -InterfaceIndex $candidate.InterfaceIndex
  $binding = Get-NetAdapterBinding -Name $adapter.Name -ComponentID ms_tcpip6
  if ($adapter.Status -ne 'Up' -or -not $binding.Enabled) { continue }
  $linkLocal = @(Get-NetIPAddress -AddressFamily IPv6 |
    Where-Object { $_.InterfaceIndex -eq $candidate.InterfaceIndex -and $_.AddressState -eq 'Preferred' -and $_.IPAddress -like 'fe80:*' })
  if ($linkLocal.Count) {
    $selected = @{ adapter=$adapter; address=($linkLocal[0].IPAddress -split '%')[0] }
    break
  }
}
if ($null -eq $selected) { throw 'No connected IPv6-enabled adapter with a preferred link-local address; IPv6 test was not run' }
$owner = [Guid]::NewGuid().ToString('N')
$segment = $owner.Substring(0,4)
$script:state = [ordered]@{
  schemaVersion=1; owner=$owner; machine=$env:COMPUTERNAME
  interfaceIndex=[int]$selected.adapter.InterfaceIndex; interfaceGuid=$selected.adapter.InterfaceGuid.ToString()
  source=([Net.IPAddress]::Parse("2001:db8:5344:${segment}::10").ToString())
  destination=([Net.IPAddress]::Parse("2001:db8:5344:${segment}::53").ToString())
  linkLocalSource=$selected.address; linkLocalDestination=([Net.IPAddress]::Parse("fe80::5344:${segment}:53").ToString())
  neighborMAC=('02-53-' + ($owner.Substring(4,8) -replace '(..)(..)(..)(..)','$1-$2-$3-$4'))
  interfaceBefore=$null; interfaceAfter=$null; sourceObserved=$null; resourcesAfterRemoval=$null; neighborRemoval=@()
  ownsSource=$false; ownsRoute=$false
  ownsDestinationNeighbor=$false; ownsLinkLocalNeighbor=$false
  setupComplete=$false; cleanupVerified=$false; error=''
}
$script:state.interfaceBefore = Get-FixtureInterface
Assert-FixtureIdentity
if (@(Get-NetIPAddress -AddressFamily IPv6 -InterfaceIndex $script:state.interfaceIndex |
    Where-Object { $_.IPAddress -in @($script:state.source,$script:state.destination,$script:state.linkLocalDestination) }).Count -or
    @(Get-FixtureRoutes).Count -or @(Get-FixtureSourceRoutes).Count -or
    @(Get-FixtureNeighbors $script:state.destination).Count -or @(Get-FixtureNeighbors $script:state.linkLocalDestination).Count) {
  throw 'Synthetic IPv6 resource collision; no existing resource was changed'
}
Save-Fixture
try {
  # netsh adds one active-store IPv6 unicast address without disabling DHCP or
  # router discovery. SkipAsSource prevents selection for unrelated VM traffic.
  $output = & "$env:SystemRoot/System32/netsh.exe" interface ipv6 add address "interface=$($script:state.interfaceIndex)" "address=$($script:state.source)/128" type=unicast store=active skipassource=true 2>&1
  if ($LASTEXITCODE -ne 0) { throw "Add synthetic IPv6 source failed: $output" }
  $script:state.ownsSource = $true
  Save-Fixture
  New-NetRoute -AddressFamily IPv6 -InterfaceIndex $script:state.interfaceIndex -DestinationPrefix "$($script:state.destination)/128" -NextHop '::' -RouteMetric 1 -PolicyStore ActiveStore | Out-Null
  $script:state.ownsRoute = $true
  Save-Fixture
  foreach ($entry in @(
    @{ address=$script:state.destination; owned='ownsDestinationNeighbor' },
    @{ address=$script:state.linkLocalDestination; owned='ownsLinkLocalNeighbor' }
  )) {
    New-NetNeighbor -InterfaceIndex $script:state.interfaceIndex -IPAddress $entry.address -LinkLayerAddress $script:state.neighborMAC -State Permanent -PolicyStore ActiveStore | Out-Null
    $script:state.($entry.owned) = $true
    Save-Fixture
  }
  $deadline = [DateTime]::UtcNow.AddSeconds(10)
  do {
    $script:state.sourceObserved = @(Get-FixtureAddresses | Select-Object IPAddress,AddressState,PrefixLength,SkipAsSource)
    $source = @($script:state.sourceObserved | Where-Object { $_.AddressState -eq 'Preferred' -and $_.PrefixLength -eq 128 -and $_.SkipAsSource })
    if ($source.Count -eq 1) { break }
    Start-Sleep -Milliseconds 200
  } while ([DateTime]::UtcNow -lt $deadline)
  Save-Fixture
  if ($source.Count -ne 1) { throw 'Synthetic IPv6 source did not become a preferred /128 SkipAsSource address' }
  Assert-InterfaceUnchanged
  $script:state.setupComplete = $true
  Save-Fixture
} catch {
  $original = $_
  $script:state.error = $_.Exception.Message
  Save-Fixture
  try { Remove-Fixture } catch { Write-Warning "IPv6 cleanup failed: $_" }
  throw $original
}
