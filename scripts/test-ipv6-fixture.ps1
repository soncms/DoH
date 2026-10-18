# Pure cleanup contract tests. Import one function's AST without evaluating
# fixture startup. Every network command/query it can call is replaced here.
param([string]$FixturePath = (Join-Path $PSScriptRoot 'ipv6-test-fixture.ps1'))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($FixturePath, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
$definition = @($ast.FindAll({ param($node)
  $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Remove-Fixture'
}, $false))
if ($definition.Count -ne 1) { throw 'Expected one cleanup function' }
$allowedCommands = @('Assert-FixtureIdentity','Assert-InterfaceUnchanged','Save-Fixture',
  'Get-FixtureAddresses','Get-FixtureRoutes','Get-FixtureSourceRoutes','Get-FixtureNeighbors',
  'Remove-NetRoute','Remove-NetIPAddress','Remove-NetNeighbor','Select-Object','Where-Object',
  'ConvertTo-Json','Start-Sleep','Clear-FixtureCache')
foreach ($command in $definition[0].FindAll({ param($node)
  $node -is [Management.Automation.Language.CommandAst]
}, $true)) {
  if ($command.GetCommandName() -notin $allowedCommands) { throw 'Cleanup gained an unmocked command; refuse execution' }
}
. ([scriptblock]::Create($definition[0].Extent.Text))

function Assert-FixtureIdentity {}
function Assert-InterfaceUnchanged {}
function Save-Fixture {}
function Get-FixtureAddresses { @() }
function Get-FixtureRoutes { @() }
function Get-FixtureSourceRoutes { @() }
function Get-FixtureNeighbors([string]$Address) { @($script:neighbors[$Address]) }
function Clear-FixtureCache([string]$Kind, [string]$Address) {
  $script:cacheCalls += [ordered]@{ kind=$Kind; address=$Address }
  if ($Kind -eq 'neighbors' -and $script:removeMode -eq 'tombstone-clear') { $script:neighbors[$Address] = @() }
}
function Remove-NetRoute { throw 'Unexpected route operation in neighbor-only contract test' }
function Remove-NetIPAddress { throw 'Unexpected address operation in neighbor-only contract test' }
function Remove-NetNeighbor {
  [CmdletBinding(SupportsShouldProcess=$true)]
  param([string]$AddressFamily, [int]$InterfaceIndex, [string]$IPAddress,
    [string]$LinkLayerAddress, [string]$State, [string]$PolicyStore,
    [Parameter(ValueFromPipeline)]$InputObject)
  process {
    $script:calls += [ordered]@{ AddressFamily=$AddressFamily; InterfaceIndex=$InterfaceIndex; IPAddress=$IPAddress;
      LinkLayerAddress=$LinkLayerAddress; State=$State; PolicyStore=$PolicyStore }
    if ($script:removeMode -eq 'clear') {
      if ($InputObject) { $IPAddress = $InputObject.IPAddress }
      $script:neighbors[$IPAddress] = @()
    }
    if ($script:removeMode -like 'tombstone-*') {
      $script:neighbors[$IPAddress] = @([pscustomobject]@{ IPAddress=$IPAddress; LinkLayerAddress='00-00-00-00-00-00'; State='Unreachable' })
    }
  }
}
function Assert-True([bool]$Value, [string]$Reason) { if (-not $Value) { throw $Reason } }
function Reset-Case {
  $script:state = [ordered]@{
    destination='2001:db8:5344:1234::53'; linkLocalDestination='fe80::5344:1234:53'
    neighborMAC='02-53-12-34-56-78'; interfaceIndex=14; neighborRemoval=@()
    ownsRoute=$false; ownsSource=$false; ownsDestinationNeighbor=$true; ownsLinkLocalNeighbor=$true
    resourcesAfterRemoval=$null; cleanupVerified=$false
  }
  $script:neighbors = @{}
  foreach ($address in @($script:state.destination, $script:state.linkLocalDestination)) {
    $script:neighbors[$address] = @([pscustomobject]@{ IPAddress=$address; LinkLayerAddress=$script:state.neighborMAC; State='Permanent' })
  }
  $script:removeMode = 'clear'; $script:calls = @(); $script:cacheCalls = @()
}

Reset-Case
Remove-Fixture
Assert-True ($script:state.cleanupVerified -and -not $script:state.ownsDestinationNeighbor -and -not $script:state.ownsLinkLocalNeighbor) 'Successful cleanup must verify absence and release ownership'
Assert-True ($script:calls.Count -eq 2) 'Expected exactly two scoped removals'
Assert-True ($script:cacheCalls.Count -eq 2 -and @($script:cacheCalls | Where-Object { $_.kind -ne 'destinationcache' }).Count -eq 0) 'Expected only two exact destination cache flushes'
foreach ($call in $script:calls) {
  Assert-True ($call.PolicyStore -eq 'ActiveStore' -and $call.AddressFamily -eq 'IPv6' -and
    $call.InterfaceIndex -eq 14 -and $call.LinkLayerAddress -eq $script:state.neighborMAC -and
    $call.State -eq 'Permanent' -and $call.IPAddress -in @($script:state.destination,$script:state.linkLocalDestination)) 'Removal escaped exact fixture keys/store'
}
Write-Output 'PASS: confirmed removal clears ownership using exact active-store keys'

Reset-Case
$script:removeMode = 'no-op'; $caught = $false
try { Remove-Fixture } catch { $caught = $true }
Assert-True ($caught -and -not $script:state.cleanupVerified -and $script:state.ownsDestinationNeighbor -and
  $script:state.ownsLinkLocalNeighbor) 'A successful no-op command must fail cleanup and retain ownership'
Write-Output 'PASS: command success without disappearance retains ownership and fails'

foreach ($drift in @('MAC','state')) {
  Reset-Case
  if ($drift -eq 'MAC') { $script:neighbors[$script:state.destination][0].LinkLayerAddress = '02-00-00-00-00-99' }
  else { $script:neighbors[$script:state.destination][0].State = 'Reachable' }
  $caught = $false
  try { Remove-Fixture } catch { $caught = $_.Exception.Message -like '*changed concurrently*' }
  Assert-True ($caught -and $script:calls.Count -eq 0 -and $script:state.ownsDestinationNeighbor -and
    -not $script:state.cleanupVerified) 'Concurrent neighbor changes must be preserved'
  Write-Output "PASS: concurrent neighbor $drift change is preserved"
}
Reset-Case
$script:neighbors[$script:state.destination] = @(); $script:neighbors[$script:state.linkLocalDestination] = @()
Remove-Fixture
Assert-True ($script:state.cleanupVerified -and $script:calls.Count -eq 0) 'Already absent resources need no mutation'
Write-Output 'PASS: already absent neighbors complete without removal calls'

Reset-Case
$script:removeMode = 'tombstone-clear'
Remove-Fixture
Assert-True ($script:state.cleanupVerified -and $script:cacheCalls.Count -eq 4) 'Zero-MAC unreachable rows must be explicitly purged and verified absent'
foreach ($call in $script:cacheCalls) {
  Assert-True ($call.address -in @($script:state.destination,$script:state.linkLocalDestination)) 'Cache flush escaped exact fixture destinations'
}
Write-Output 'PASS: unreachable zero-MAC cache is purged only at exact fixture destinations'

Reset-Case
$script:removeMode = 'tombstone-stuck'; $caught = $false
try { Remove-Fixture } catch { $caught = $true }
Assert-True ($caught -and -not $script:state.cleanupVerified -and $script:state.ownsDestinationNeighbor) 'A surviving unreachable cache row must still fail cleanup'
Write-Output 'PASS: surviving unreachable cache remains a cleanup failure'
