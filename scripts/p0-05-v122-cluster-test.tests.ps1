[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$scriptPath = Join-Path $PSScriptRoot 'p0-05-v122-cluster-test.ps1'
$text = Get-Content -LiteralPath $scriptPath -Raw -Encoding utf8
$coreBridgeText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-core-bridge.ps1') -Raw -Encoding utf8
$clusterGoText = Get-Content -LiteralPath (Join-Path $worktreeRoot 'internal\e2e\p0_05_v122_cluster_helpers_test.go') -Raw -Encoding utf8
$upText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-up.ps1') -Raw -Encoding utf8
$downText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-down.ps1') -Raw -Encoding utf8

function Assert-True {
    param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)
    if (-not $Condition) { throw $Message }
}

$tokens = $null
$errors = $null
[void][Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
Assert-True ($errors.Count -eq 0) 'cluster E2E script must parse in Windows PowerShell 5.1'
Assert-True ($text.Contains("BLOCKED_CLUSTER_TEST_PATTERN") -and $text.Contains('-run $TestPattern')) 'cluster E2E must validate and pass through its explicit test pattern'
Assert-True ($text.Contains('-timeout 30m')) 'combined real E2E must exceed the Go default package timeout without changing per-test budgets'
Assert-True ($text.Contains('[switch]$AllowPartialEvidence')) 'filtered cluster E2E must use an explicit partial-evidence validation mode'
Assert-True ($text.Contains("`$TestPattern -cne 'TestP005V122'")) 'only the complete cluster suite may claim full evidence completeness'

foreach ($required in @(
    'p0-05-v122-env.ps1', 'Import-P005V122LocalEnvironment',
    'Get-P005V122LoopbackPort', 'Start-P005V122CoreBridgePortForward', 'Start-P005V122MCPGatewayPortForward', 'Start-P005V122MatrixPortForward', 'Stop-P005V122CoreBridgePortForward',
    'HAOWORK_P005_CLUSTER_CORE_BRIDGE_READY_URL', 'HAOWORK_P005_CLUSTER_CORE_BRIDGE_TOKEN',
    'HAOWORK_P005_CLUSTER_MCP_GATEWAY_URL',
    'HAOWORK_P005_CLUSTER_MATRIX_URL',
    'HAOWORK_P005_CLUSTER_EXECUTION_ID', 'HAOWORK_P005_CLUSTER_MISSION_ID', 'HAOWORK_P005_CLUSTER_CONTROLLER_NAME', 'HAOWORK_P005_CLUSTER_MODEL',
    'HAOWORK_P005_CLUSTER_MANAGER_RUNTIME', 'HAOWORK_P005_CLUSTER_WORKER_RUNTIME',
    'HAOWORK_P005_CLUSTER_MANAGER_IMAGE',
    'HAOWORK_P005_CLUSTER_MCP_URL', 'HAOWORK_P005_CLUSTER_MCP_SERVER_NAME', 'HAOWORK_P005_CLUSTER_MCP_TRANSPORT', 'HAOWORK_P005_CLUSTER_HUMAN_NAME',
    'haowork-core-bridge-runtime', 'haowork-public'
)) {
    Assert-True ($text.Contains($required)) "cluster E2E script omits $required"
}
Assert-True ($text.Contains('$forwardArguments') -and $text.Contains('-ArgumentList $forwardArguments')) 'port-forward worker must pass one quoted command line for paths with spaces'
Assert-True ($text -match 'function\s+Start-P005V122MatrixPortForward[\s\S]*?\[Parameter\(Mandatory\)\]\[string\]\$Service') 'Matrix port-forward wrapper must declare the service identity passed by both zones'
Assert-True ($text.Contains('"http://127.0.0.1:$mcpGatewayPort/mcp-servers/haowork-mcp"')) 'cluster E2E must call the deployed Higress MCP route prefix'
Assert-True ($text.Contains("-Name 'dispatch_event_id'")) 'cluster evidence must validate the opaque Matrix dispatch event'
Assert-True ($text.Contains('function Get-P005V122SHA256') -and -not $text.Contains('Get-FileHash')) 'cluster evidence manifest must use its own PowerShell 5.1-compatible SHA-256 implementation'
Assert-True ($coreBridgeText.Contains('submit_execution_report')) 'Core Bridge must dispatch the receipt tool contract'
Assert-True ($clusterGoText.Contains('{Name: "submit_execution_report", Version: "1.0.0"}')) 'real Mission must grant the Delivery Leader receipt Skill'
Assert-True ($clusterGoText.Contains('missionEnvelopeForEnvironment(fixture.publicZone.name, config.missionID)')) 'zone-switched topology must bind every official resource to the active zone environment'
foreach ($required in @(
    './cmd/haowork-agentteams-binding-controller', './cmd/haowork-higress-binding-publisher',
    'haowork-binding-controller.yaml', 'haowork-binding-controller-internal.yaml',
    'haowork-higress-binding-publisher.yaml', 'haowork-higress-binding-publisher-internal.yaml',
    'deployment/haowork-agentteams-binding-controller', 'deployment/haowork-higress-binding-publisher'
)) {
    Assert-True ($coreBridgeText.Contains($required)) "Core Bridge deployment omits structured receipt component $required"
}
Assert-True ($coreBridgeText.Contains("foreach (`$namespace in @('haowork-public', 'haowork-internal'))")) 'Core Bridge deployment must restart local-image workloads in both zones'
foreach ($deployment in @('haowork-core-bridge', 'haowork-mcp', 'haowork-agentteams-binding-controller', 'haowork-higress-binding-publisher', 'haowork-network-probe')) {
    Assert-True ($coreBridgeText.Contains("'$deployment'")) "Core Bridge deployment restart set omits $deployment"
}
Assert-True ($coreBridgeText.Contains('@(''rollout'', ''restart'', "deployment/$deployment", ''-n'', $namespace)')) 'Core Bridge deployment must restart each local-image workload before rollout checks'
Assert-True ($coreBridgeText.Contains('Wait-P005V122LocalDeploymentObserved')) 'Core Bridge deployment must observe each restarted local-image workload'
Assert-True ($coreBridgeText.Contains('Wait-P005V122LocalDeploymentObserved -Kubectl $kubectl -Namespace $namespace -Deployment $deployment')) 'Core Bridge deployment must verify both-zone restart generations before returning'
Assert-True ($text -notmatch 'if\s*\(\$AllowPartialEvidence\)\s*\{\s*Assert-P005V122SafeEvidenceValue[\s\S]*?return') 'filtered evidence must not bypass semantic validation for present sections'
foreach ($forbidden in @('$gatewayKeyEncoded', "consumer_name = 'manager'", "allowedConsumers = @('manager')", "consumers = @('manager')")) {
    Assert-True (-not $coreBridgeText.Contains($forbidden)) "Core Bridge deployment retains obsolete Manager receipt binding: $forbidden"
}
Assert-True ($text.Contains('execution_receipt')) 'cluster E2E must require a structured execution receipt'
Assert-True (-not $text.Contains('HAOWORK_CORRELATION_ID')) 'cluster E2E must not depend on text correlation'
Assert-True ($text.Contains('assignment_id') -and $text.Contains('receipt_id') -and $text.Contains('binding_revision')) 'cluster evidence must carry structured assignment and receipt identity'
Assert-True ($text.Contains('dispatch_event_id') -and $text.Contains('room_kind')) 'cluster evidence must retain only the Matrix dispatch envelope'
Assert-True (-not $text.Contains('sender_id') -and -not $text.Contains('room_id')) 'cluster evidence must not retain Matrix sender or room identifiers'
Assert-True (-not $text.Contains('gateway_key') -and -not $text.Contains('WORKER_GATEWAY_KEY')) 'cluster evidence must not retain Gateway credentials'
Assert-True ($text.Contains('BLOCKED_')) 'cluster E2E must expose explicit blockers'
Assert-True ($text.Contains("throw 'BLOCKED_RUNNER_LOCAL_ENV'") -and $text.Contains("throw 'BLOCKED_KUBECONFIG'")) 'cluster E2E must fail closed when runner environment or kubeconfig is missing'
Assert-True ($text.Contains('publicEnvironmentFields') -and $text.Contains('internalEnvironmentFields')) 'cluster E2E must validate both public and internal provider configuration itself'
Assert-True ($text.Contains("throw 'BLOCKED_INTERNAL_ENV'")) 'cluster E2E must fail closed when internal-zone provider configuration is missing'
Assert-True ($text.Contains("$internalManagerImage -cne $lockedManagerImage")) 'cluster E2E must compare internal Manager image with the official digest lock'
Assert-True (-not $text.Contains('NOT_RUN: runner-local AgentTeams environment is not configured') -and -not $text.Contains('NOT_RUN: owned Kind cluster kubeconfig is not available')) 'cluster E2E must not downgrade missing prerequisites to NOT_RUN'
Assert-True ($text.Contains('Get-P005V122LockedImageReference') -and $coreBridgeText.Contains('Get-P005V122LockedImageReference')) 'cluster and Core Bridge deployment must derive the Manager image from the official lock'
Assert-True ($text.Contains("@.name=='worker'") -and $coreBridgeText.Contains("name -ceq 'worker'")) 'Manager image discovery must select the official worker-named Manager container explicitly'
Assert-True ($text.Contains("([string](& `$kubectl get secret haowork-core-bridge-runtime")) 'missing Core Bridge Secret output must be converted before Trim so the explicit blocker is preserved'
Assert-True (-not $coreBridgeText.Contains('core-bridge.token')) 'Core Bridge must not persist its bearer token outside Kubernetes'
Assert-True ($coreBridgeText.Contains('Write-P005V122SecretFile')) 'Core Bridge must atomically create its temporary Secret input file with a protected ACL'
Assert-True ($coreBridgeText.Contains('haowork-public-agentteams-runtime-env')) 'Core Bridge must derive Matrix AppService credentials from the official release runtime Secret'
Assert-True ($coreBridgeText.Contains('AGENTTEAMS_MATRIX_APPSERVICE_AS_TOKEN')) 'Core Bridge must read the official AgentTeams AppService token key'
Assert-True ($coreBridgeText.Contains('matrix-appservice-as-token')) 'Core Bridge must inject the AppService token only through its Kubernetes runtime Secret'
Assert-True ($coreBridgeText.Contains("([string](& `$kubectl get secret haowork-public-agentteams-runtime-env")) 'missing AppService Secret output must be converted before Trim so the explicit blocker is preserved'
Assert-True ($coreBridgeText.Contains('ConvertFrom-P005V122AppServiceToken')) 'Core Bridge must normalize malformed AppService credentials to an explicit blocker'
foreach ($required in @('haowork-network-probe', 'Dockerfile.network-probe', 'haowork-network-probe-public.yaml', 'haowork-network-probe-internal.yaml')) {
    Assert-True ($coreBridgeText.Contains($required)) "Core Bridge deployment omits network probe dependency $required"
}
$runtimeDisposeIndex = $coreBridgeText.IndexOf('if ($null -ne $runtimeEnvironmentHandle) { $runtimeEnvironmentHandle.Dispose() }', [StringComparison]::Ordinal)
$runtimeRemoveIndex = $coreBridgeText.IndexOf('Remove-Item -LiteralPath $runtimeEnvironmentPath', $runtimeDisposeIndex, [StringComparison]::Ordinal)
Assert-True ($runtimeDisposeIndex -ge 0 -and $runtimeRemoveIndex -gt $runtimeDisposeIndex) 'Core Bridge must close and delete its temporary Secret input file in finally'
$internalDisposeIndex = $coreBridgeText.IndexOf('if ($null -ne $internalEnvironmentHandle) { $internalEnvironmentHandle.Dispose() }', [StringComparison]::Ordinal)
$internalRemoveIndex = $coreBridgeText.IndexOf("throw 'BLOCKED_CORE_BRIDGE_SECRET_CLEANUP_INTERNAL'", $internalDisposeIndex, [StringComparison]::Ordinal)
Assert-True ($internalDisposeIndex -ge 0 -and $internalRemoveIndex -gt $internalDisposeIndex) 'Core Bridge must fail closed if internal temporary Secret cleanup fails'
if ($env:OS -eq 'Windows_NT' -and $PSVersionTable.PSEdition -eq 'Desktop') {
    $coreMarker = '$worktreeRoot = Split-Path -Parent $PSScriptRoot'
    $coreMarkerIndex = $coreBridgeText.IndexOf($coreMarker, [StringComparison]::Ordinal)
    Assert-True ($coreMarkerIndex -gt 0) 'Core Bridge script must retain a function-only loading boundary'
    Invoke-Expression $coreBridgeText.Substring(0, $coreMarkerIndex)
	foreach ($encodedToken in @('', 'not-base64', [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("token`nsecond-line")))) {
		$blocked = $false
		try { ConvertFrom-P005V122AppServiceToken -EncodedValue $encodedToken | Out-Null } catch { $blocked = $_.Exception.Message -eq 'BLOCKED_MATRIX_APPSERVICE_TOKEN' }
		Assert-True $blocked 'invalid AppService token input must return BLOCKED_MATRIX_APPSERVICE_TOKEN'
	}
    $secretFixture = Join-Path $worktreeRoot '.haowork\cache\tmp\p0-05-core-bridge-secret-file.env'
    New-Item -ItemType Directory -Force (Split-Path $secretFixture -Parent) | Out-Null
    $handle = $null
    try {
        $handle = Write-P005V122SecretFile -Path $secretFixture -Lines @('token=test-only')
        $acl = Get-Acl -LiteralPath $secretFixture
        Assert-True ($acl.AreAccessRulesProtected) 'Core Bridge temporary Secret file inherited an ambient ACL'
        Assert-True ([IO.File]::ReadAllText($secretFixture).Contains('token=test-only')) 'Core Bridge security handle blocked the kubectl-style concurrent reader'
    } finally {
        if ($null -ne $handle) { $handle.Dispose() }
        Remove-Item -LiteralPath $secretFixture -Force -ErrorAction SilentlyContinue
    }
}
foreach ($lifecycleText in @($upText, $downText)) {
    Assert-True (-not $lifecycleText.Contains('BrowserPortForward') -and -not $lifecycleText.Contains('process_start_time_utc')) 'deployment lifecycle must not retain persistent browser process management'
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
$unsafeSummary = ConvertTo-P005V122EvidenceSummary -Document ([pscustomobject]@{
    apiVersion = 'v1'; kind = 'Pod'; items = $null
    metadata = [pscustomobject]@{ name = 'fixture'; namespace = 'haowork-public'; labels = [pscustomobject]@{ address = '10.0.0.7'; endpoint = 'higress-console.haowork-public.svc.cluster.local:8080' } }
    spec = [pscustomobject]@{ containers = @() }
    status = [pscustomobject]@{ message = 'private path C:\\private\\runtime' }
})
Assert-True ($unsafeSummary.items[0].metadata.labels.address -eq '[REDACTED]' -and
    $unsafeSummary.items[0].metadata.labels.endpoint -eq '[REDACTED]' -and
    $unsafeSummary.items[0].status.message -eq '[REDACTED]') 'cluster summary must redact private IP, host and path values'

function New-P005V122EvidenceFixture {
    $hash = 'a' * 64
    return [ordered]@{
        schema_version = 1
        baseline = [ordered]@{
            cluster_name = 'haowork-p005-v122'
            contract = [ordered]@{ tag = 'v1.2.2'; commit = '849182af8e017168a5a200a87b1062142caf462d'; chart_version = '1.1.1' }
            crd_kinds = @('Human', 'Manager', 'Team', 'Worker')
            zones = @(
                [ordered]@{ name = 'public'; namespace = 'haowork-public'; release = 'haowork-public-agentteams' },
                [ordered]@{ name = 'internal'; namespace = 'haowork-internal'; release = 'haowork-internal-agentteams' }
            )
        }
        topology = [ordered]@{ team_name = 'mission-team'; room_kind = 'leader_dm'; agent_roles = @('manager', 'delivery_leader', 'research', 'build', 'verify'); zone = 'public' }
        skills = [ordered]@{ research = @('advisory'); build = @('patch'); verify = @('audit') }
        data_path = [ordered]@{
            assignment = [ordered]@{ assignment_id = 'ASN-46fc8933c009dd44765c6f4b01aeaf9d'; state = 'REPORT_SUBMITTED' }
            matrix = [ordered]@{ dispatch_event_id = '$dispatch'; room_kind = 'leader_dm' }
            receipt = [ordered]@{ receipt_id = 'ERC-001'; report_sha256 = $hash; leader_function = 'delivery_leader'; binding_revision = 1 }
            artifact = [ordered]@{ uri = 's3://haowork-public-artifacts/evidence.json'; sha256 = $hash; size = 1234; environment_id = 'public' }
            mcp = [ordered]@{ server_name = 'haowork-mcp'; consumer_kind = 'agentteams_worker'; trace_sha256 = $hash }
        }
        network_policy = [ordered]@{ public_to_internal = 'denied'; internal_to_public = 'denied' }
        restart = [ordered]@{ controller = 'core_bridge'; start_new_state = 'FRESH_RUN'; receipt_id = 'ERC-002' }
    }
}

function Write-P005V122EvidenceFixture {
    param([Parameter(Mandatory)][object]$Value, [Parameter(Mandatory)][string]$Path)
    $Value | ConvertTo-Json -Depth 32 | Set-Content -LiteralPath $Path -Encoding utf8
}

$evidenceFixturePath = Join-Path $worktreeRoot '.haowork\cache\tmp\p0-05-v122-evidence-fixture.json'
New-Item -ItemType Directory -Force (Split-Path -Parent $evidenceFixturePath) | Out-Null
try {
    $normal = New-P005V122EvidenceFixture
    Write-P005V122EvidenceFixture -Value $normal -Path $evidenceFixturePath
    Assert-P005V122EvidenceDocument -Path $evidenceFixturePath | Out-Null
    foreach ($sensitiveValue in @('Bearer runtime-value', 'eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZA.c2lnbmF0dXJl', 'password=LEAK', 'apiKey=LEAK', 'C:\secret', '/home/operator/private.json')) {
        $blocked = $false
        try { Assert-P005V122SafeEvidenceValue -Value $sensitiveValue -Path '$.fixture' } catch { $blocked = $_.Exception.Message -eq 'BLOCKED_CLUSTER_EVIDENCE_SENSITIVE_DATA' }
        Assert-True $blocked "cluster evidence must reject credential-shaped value $sensitiveValue"
    }
    $schemaDocument = Get-Content -LiteralPath (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\cluster-evidence.schema.json') -Raw -Encoding utf8 | ConvertFrom-Json
    $schemaS3Pattern = [string]$schemaDocument.properties.data_path.properties.artifact.properties.uri.pattern
    foreach ($acceptedURI in @(
        's3://canonical-bucket/restricted/path',
        's3://canonical-bucket/restricted/path-01.json',
        's3://canonical-bucket/restricted_path/report.sha256'
    )) {
        Assert-True (Test-P005V122S3ArtifactURI -Value $acceptedURI) "runtime rejected canonical S3 URI $acceptedURI"
        Assert-True ($acceptedURI -cmatch $schemaS3Pattern) "schema rejected canonical S3 URI $acceptedURI"
    }
    $rejectedURIs = @(
        's3://alice:p4ss@bucket/path',
        's3:///path',
        's3://bucket/path\secret',
        's3://bucket/../secret',
        's3://bucket/%2e%2e/secret',
        's3://bucket/%2E%2E/secret',
        's3://bucket/path%2fsecret',
        's3://bucket/path%2Fsecret',
        's3://bucket/%2520secret',
        's3://bucket/path%20with-space',
        's3://bucket/restricted//path',
        's3://bucket/restricted/path?query=1',
        's3://bucket/restricted/path#fragment'
    )
    foreach ($rejectedURI in $rejectedURIs) {
        Assert-True (-not (Test-P005V122S3ArtifactURI -Value $rejectedURI)) "runtime accepted unsafe S3 URI $rejectedURI"
        Assert-True (-not ($rejectedURI -cmatch $schemaS3Pattern)) "schema accepted unsafe S3 URI $rejectedURI"
    }
    foreach ($benignName in @('token-service', 'password-reset', 'api-key-migration')) {
        $benign = (New-P005V122EvidenceFixture | ConvertTo-Json -Depth 32 | ConvertFrom-Json)
        $benign.topology.team_name = $benignName
        $benign.data_path.artifact.uri = "s3://haowork-public-artifacts/$benignName/report.json"
        Write-P005V122EvidenceFixture -Value $benign -Path $evidenceFixturePath
        Assert-P005V122EvidenceDocument -Path $evidenceFixturePath | Out-Null
    }
    foreach ($unsafeURI in $rejectedURIs) {
        $malicious = (New-P005V122EvidenceFixture | ConvertTo-Json -Depth 32 | ConvertFrom-Json)
        $malicious.data_path.artifact.uri = $unsafeURI
        Write-P005V122EvidenceFixture -Value $malicious -Path $evidenceFixturePath
        $blocked = $false
        try { Assert-P005V122EvidenceDocument -Path $evidenceFixturePath | Out-Null } catch { $blocked = $_.Exception.Message -eq 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_URI' }
        Assert-True $blocked "cluster evidence must reject unsafe S3 URI $unsafeURI"
    }
    foreach ($case in @(
        @{ Section = 'baseline'; Name = 'details'; Value = 'C:\secret' },
        @{ Section = 'skills'; Name = 'apiKey'; Value = 'LEAK' },
        @{ Section = 'restart'; Name = 'correlation_id'; Value = 'legacy-correlation' }
    )) {
        $malicious = (New-P005V122EvidenceFixture | ConvertTo-Json -Depth 32 | ConvertFrom-Json)
        $malicious.($case.Section) | Add-Member -NotePropertyName $case.Name -NotePropertyValue $case.Value
        Write-P005V122EvidenceFixture -Value $malicious -Path $evidenceFixturePath
        $blocked = $false
        try { Assert-P005V122EvidenceDocument -Path $evidenceFixturePath | Out-Null } catch { $blocked = $_.Exception.Message -like 'BLOCKED_CLUSTER_EVIDENCE_*' }
        Assert-True $blocked "cluster evidence must reject $($case.Name) hidden in $($case.Section)"
    }
    foreach ($case in @(
        @{ Section = 'data_path.matrix'; Name = 'dispatch_event_id'; Value = '$C:\secret' },
        @{ Section = 'data_path.matrix'; Name = 'dispatch_event_id'; Value = '$file:/home/operator/private.json' },
        @{ Section = 'data_path.artifact'; Name = 'uri'; Value = 's3://bucket/10.0.0.7' },
        @{ Section = 'data_path.artifact'; Name = 'uri'; Value = 's3://bucket/service.haowork-public.svc.cluster.local' }
    )) {
        $malicious = (New-P005V122EvidenceFixture | ConvertTo-Json -Depth 32 | ConvertFrom-Json)
        $target = $malicious
        foreach ($segment in $case.Section.Split('.')) { $target = $target.$segment }
        $target.($case.Name) = $case.Value
        Write-P005V122EvidenceFixture -Value $malicious -Path $evidenceFixturePath
        $blocked = $false
        try { Assert-P005V122EvidenceDocument -Path $evidenceFixturePath | Out-Null } catch { $blocked = $_.Exception.Message -eq 'BLOCKED_CLUSTER_EVIDENCE_SENSITIVE_DATA' }
        Assert-True $blocked "cluster evidence must reject sensitive value $($case.Value)"
    }
} finally {
    Remove-Item -LiteralPath $evidenceFixturePath -Force -ErrorAction SilentlyContinue
}

function Assert-P005V122SchemaObjectsClosed {
    param([AllowNull()][object]$Node, [string]$Path = '$')
    if ($null -eq $Node) { return }
    if ($Node -is [System.Management.Automation.PSCustomObject]) {
        if ([string]$Node.type -ceq 'object') {
            Assert-True ($null -ne $Node.PSObject.Properties['additionalProperties'] -and $Node.additionalProperties -eq $false) "schema object is open at $Path"
        }
        foreach ($property in $Node.PSObject.Properties) {
            Assert-P005V122SchemaObjectsClosed -Node $property.Value -Path "$Path.$($property.Name)"
        }
    } elseif ($Node -is [System.Collections.IEnumerable] -and -not ($Node -is [string])) {
        $index = 0
        foreach ($item in $Node) {
            Assert-P005V122SchemaObjectsClosed -Node $item -Path "$Path[$index]"
            $index++
        }
    }
}
Assert-P005V122SchemaObjectsClosed -Node $schemaDocument

Write-Output 'AgentTeams v1.2.2 cluster E2E automation contract tests passed.'
