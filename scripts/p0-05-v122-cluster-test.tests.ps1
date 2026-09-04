[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$scriptPath = Join-Path $PSScriptRoot 'p0-05-v122-cluster-test.ps1'
$text = Get-Content -LiteralPath $scriptPath -Raw -Encoding utf8
$coreBridgeText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-core-bridge.ps1') -Raw -Encoding utf8

function Assert-True {
    param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)
    if (-not $Condition) { throw $Message }
}

$tokens = $null
$errors = $null
[void][Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
Assert-True ($errors.Count -eq 0) 'cluster E2E script must parse in Windows PowerShell 5.1'

foreach ($required in @(
    'p0-05-v122-env.ps1', 'Import-P005V122LocalEnvironment',
    'Get-P005V122LoopbackPort', 'Start-P005V122CoreBridgePortForward', 'Start-P005V122MCPGatewayPortForward', 'Stop-P005V122CoreBridgePortForward',
    'HAOWORK_P005_CLUSTER_CORE_BRIDGE_READY_URL', 'HAOWORK_P005_CLUSTER_CORE_BRIDGE_TOKEN',
    'HAOWORK_P005_CLUSTER_MCP_GATEWAY_URL',
    'HAOWORK_P005_CLUSTER_EXECUTION_ID', 'HAOWORK_P005_CLUSTER_MISSION_ID', 'HAOWORK_P005_CLUSTER_CONTROLLER_NAME', 'HAOWORK_P005_CLUSTER_MODEL',
    'HAOWORK_P005_CLUSTER_MANAGER_RUNTIME', 'HAOWORK_P005_CLUSTER_WORKER_RUNTIME',
    'HAOWORK_P005_CLUSTER_MCP_URL', 'HAOWORK_P005_CLUSTER_MCP_SERVER_NAME', 'HAOWORK_P005_CLUSTER_MCP_TRANSPORT', 'HAOWORK_P005_CLUSTER_HUMAN_NAME',
    'haowork-core-bridge-runtime', 'haowork-public'
)) {
    Assert-True ($text.Contains($required)) "cluster E2E script omits $required"
}
Assert-True ($text.Contains('$forwardArguments') -and $text.Contains('-ArgumentList $forwardArguments')) 'port-forward worker must pass one quoted command line for paths with spaces'
Assert-True ($text.Contains('"http://127.0.0.1:$mcpGatewayPort/mcp-servers/haowork-mcp"')) 'cluster E2E must call the deployed Higress MCP route prefix'
Assert-True ($text.Contains("-Name 'route_name' -Pattern '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.internal$'")) 'cluster evidence must validate the official Higress internal route name separately from DNS-label fields'
Assert-True ($text.Contains('function Get-P005V122SHA256') -and -not $text.Contains('Get-FileHash')) 'cluster evidence manifest must use its own PowerShell 5.1-compatible SHA-256 implementation'
Assert-True ($text.Contains("([string](& `$kubectl get secret haowork-core-bridge-runtime")) 'missing Core Bridge Secret output must be converted before Trim so the explicit blocker is preserved'
Assert-True (-not $coreBridgeText.Contains('core-bridge.token')) 'Core Bridge must not persist its bearer token outside Kubernetes'
Assert-True ($coreBridgeText.Contains('Write-P005V122SecretFile')) 'Core Bridge must atomically create its temporary Secret input file with a protected ACL'
$runtimeDisposeIndex = $coreBridgeText.IndexOf('if ($null -ne $runtimeEnvironmentHandle) { $runtimeEnvironmentHandle.Dispose() }', [StringComparison]::Ordinal)
$runtimeRemoveIndex = $coreBridgeText.IndexOf('Remove-Item -LiteralPath $runtimeEnvironmentPath', $runtimeDisposeIndex, [StringComparison]::Ordinal)
Assert-True ($runtimeDisposeIndex -ge 0 -and $runtimeRemoveIndex -gt $runtimeDisposeIndex) 'Core Bridge must close and delete its temporary Secret input file in finally'
if ($env:OS -eq 'Windows_NT' -and $PSVersionTable.PSEdition -eq 'Desktop') {
    $coreMarker = '$worktreeRoot = Split-Path -Parent $PSScriptRoot'
    $coreMarkerIndex = $coreBridgeText.IndexOf($coreMarker, [StringComparison]::Ordinal)
    Assert-True ($coreMarkerIndex -gt 0) 'Core Bridge script must retain a function-only loading boundary'
    Invoke-Expression $coreBridgeText.Substring(0, $coreMarkerIndex)
    $secretFixture = Join-Path $worktreeRoot '.haowork\cache\tmp\p0-05-core-bridge-secret-file.env'
    New-Item -ItemType Directory -Force (Split-Path $secretFixture -Parent) | Out-Null
    $handle = $null
    try {
        $handle = Write-P005V122SecretFile -Path $secretFixture -Lines @('token=test-only')
        $acl = Get-Acl -LiteralPath $secretFixture
        Assert-True ($acl.AreAccessRulesProtected) 'Core Bridge temporary Secret file inherited an ambient ACL'
    } finally {
        if ($null -ne $handle) { $handle.Dispose() }
        Remove-Item -LiteralPath $secretFixture -Force -ErrorAction SilentlyContinue
    }
}
foreach ($forbidden in @('HAOWORK_P005_CLUSTER_PUBLIC_DENIED_TARGETS', 'HAOWORK_P005_CLUSTER_INTERNAL_DENIED_TARGETS', 'HAOWORK_P005_CLUSTER_PUBLIC_PROBE_POD', 'HAOWORK_P005_CLUSTER_INTERNAL_PROBE_POD')) {
    Assert-True (-not $text.Contains($forbidden)) "cluster E2E script retains obsolete manual probe input $forbidden"
}

$marker = '$ClusterName = Assert-P005V122ClusterName -Name $ClusterName'
$markerIndex = $text.IndexOf($marker, [StringComparison]::Ordinal)
Assert-True ($markerIndex -gt 0) 'cluster E2E script must retain a function-only loading boundary'
Invoke-Expression $text.Substring(0, $markerIndex)
$crdDocument = [pscustomobject]@{
    apiVersion = 'v1'
    kind = 'List'
    items = @([pscustomobject]@{
        kind = 'CustomResourceDefinition'
        metadata = [pscustomobject]@{ name = 'managers.agentteams.io'; namespace = $null; labels = $null }
        status = $null
    })
}
$crdSummary = ConvertTo-P005V122EvidenceSummary -Document $crdDocument
Assert-True ($crdSummary.items.Count -eq 1 -and $null -eq $crdSummary.items[0].metadata.labels -and $null -eq $crdSummary.items[0].status) 'cluster evidence summary must accept CRDs with absent optional labels and status'

Write-Output 'AgentTeams v1.2.2 cluster E2E automation contract tests passed.'
