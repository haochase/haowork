[CmdletBinding()]
param(
    [string]$ClusterName = 'haowork-p005-v122',
    [string]$TestPattern = 'TestP005V122'
)

$ErrorActionPreference = 'Stop'

if ($TestPattern -notmatch '^TestP005V122(?:[A-Za-z0-9|()]+)?$') { throw 'BLOCKED_CLUSTER_TEST_PATTERN' }

function Assert-P005V122ClusterName {
    param([Parameter(Mandatory)][string]$Name)

    if ($Name -notmatch '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$') {
        throw 'BLOCKED_KIND_CLUSTER_NAME'
    }
    return $Name
}

function Resolve-P005V122RepoRoot {
    param([Parameter(Mandatory)][string]$WorktreeRoot)

    $commonGitDir = (& git -C $WorktreeRoot rev-parse --git-common-dir).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_GIT_WORKTREE' }
    return Split-Path -Parent ([IO.Path]::GetFullPath($commonGitDir))
}

function Resolve-P005V122Executable {
    param(
        [Parameter(Mandatory)][string]$RepoRoot,
        [Parameter(Mandatory)][string]$Name
    )

    $local = Join-Path $RepoRoot ".tools\bin\$Name.exe"
    if (Test-Path -LiteralPath $local -PathType Leaf) { return $local }
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    throw "BLOCKED_$($Name.ToUpperInvariant())"
}

function ConvertFrom-P005V122Base64 {
    param([Parameter(Mandatory)][string]$Value)
    return [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Value))
}

function Get-P005V122LoopbackPort {
    $listener = New-Object Net.Sockets.TcpListener ([Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

function Start-P005V122CoreBridgePortForward {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$Kubeconfig,
        [Parameter(Mandatory)][string]$RuntimeRoot,
        [Parameter(Mandatory)][int]$LocalPort,
        [string]$Namespace = 'haowork-public',
        [string]$Service = 'haowork-core-bridge'
    )

    $workerPath = Join-Path $RuntimeRoot 'core-bridge-port-forward.ps1'
    @'
param(
    [Parameter(Mandatory)][string]$Kubectl,
    [Parameter(Mandatory)][string]$Kubeconfig,
    [Parameter(Mandatory)][int]$LocalPort,
    [Parameter(Mandatory)][string]$Namespace,
    [Parameter(Mandatory)][string]$Service
)
$ErrorActionPreference = 'Continue'
$env:KUBECONFIG = $Kubeconfig
while ($true) {
    & $Kubectl port-forward -n $Namespace service/$Service "$LocalPort`:8081"
    Start-Sleep -Seconds 1
}
'@ | Set-Content -LiteralPath $workerPath -Encoding utf8
    $forwardArguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $workerPath + '" -Kubectl "' + $Kubectl + '" -Kubeconfig "' + $Kubeconfig + '" -LocalPort ' + $LocalPort + ' -Namespace "' + $Namespace + '" -Service "' + $Service + '"'
    $process = Start-Process -FilePath powershell.exe -ArgumentList $forwardArguments -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $RuntimeRoot 'core-bridge-port-forward.out.log') -RedirectStandardError (Join-Path $RuntimeRoot 'core-bridge-port-forward.err.log')
    return [pscustomobject]@{ Process = $process; WorkerPath = $workerPath }
}

function Start-P005V122MCPGatewayPortForward {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$Kubeconfig,
        [Parameter(Mandatory)][string]$RuntimeRoot,
        [Parameter(Mandatory)][int]$LocalPort,
        [string]$Namespace = 'haowork-public'
    )

    $workerPath = Join-Path $RuntimeRoot 'mcp-gateway-port-forward.ps1'
    @'
param(
    [Parameter(Mandatory)][string]$Kubectl,
    [Parameter(Mandatory)][string]$Kubeconfig,
    [Parameter(Mandatory)][int]$LocalPort,
    [Parameter(Mandatory)][string]$Namespace
)
$ErrorActionPreference = 'Continue'
$env:KUBECONFIG = $Kubeconfig
while ($true) {
    & $Kubectl port-forward -n $Namespace service/higress-gateway "$LocalPort`:80"
    Start-Sleep -Seconds 1
}
'@ | Set-Content -LiteralPath $workerPath -Encoding utf8
    $forwardArguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $workerPath + '" -Kubectl "' + $Kubectl + '" -Kubeconfig "' + $Kubeconfig + '" -LocalPort ' + $LocalPort + ' -Namespace "' + $Namespace + '"'
    $process = Start-Process -FilePath powershell.exe -ArgumentList $forwardArguments -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $RuntimeRoot 'mcp-gateway-port-forward.out.log') -RedirectStandardError (Join-Path $RuntimeRoot 'mcp-gateway-port-forward.err.log')
    return [pscustomobject]@{ Process = $process; WorkerPath = $workerPath }
}

function Wait-P005V122CoreBridgeReady {
    param(
        [Parameter(Mandatory)][string]$Token,
        [Parameter(Mandatory)][string]$ReadyURL
    )

    for ($attempt = 0; $attempt -lt 80; $attempt++) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Headers @{ Authorization = "Bearer $Token" } -Uri $ReadyURL -TimeoutSec 2
            $ready = $response.Content | ConvertFrom-Json -ErrorAction Stop
            if ($response.StatusCode -eq 200 -and $ready.mission_resolver_ready -eq $true -and $ready.runtime_binding_store_ready -eq $true -and $ready.trace_store_ready -eq $true -and $ready.production_transport_ready -eq $true) {
                return
            }
        } catch {}
        Start-Sleep -Milliseconds 500
    }
    throw 'BLOCKED_HAOWORK_CORE_BRIDGE_PORT_FORWARD'
}

function Stop-P005V122CoreBridgePortForward {
    param([AllowNull()][object]$Forward)

    if ($null -eq $Forward) { return }
    if ($null -ne $Forward.Process -and -not $Forward.Process.HasExited) {
        & "$env:SystemRoot\System32\taskkill.exe" /PID $Forward.Process.Id /T /F | Out-Null
    }
    if (Test-Path -LiteralPath $Forward.WorkerPath) { Remove-Item -LiteralPath $Forward.WorkerPath -Force }
}

function Invoke-P005V122KubectlEvidence {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Blocker
    )

    $output = & $Kubectl @Arguments
    if ($LASTEXITCODE -ne 0) { throw $Blocker }
    try {
        $document = ($output -join "`n") | ConvertFrom-Json -ErrorAction Stop
        ConvertTo-P005V122EvidenceSummary -Document $document | ConvertTo-Json -Depth 16 | Set-Content -LiteralPath $Path -Encoding utf8
    } catch {
        throw $Blocker
    }
}

function ConvertTo-P005V122EvidenceSummary {
    param([Parameter(Mandatory)][object]$Document)

    $items = if ($Document.kind -eq 'List') { @($Document.items) } else { @($Document) }
    return [ordered]@{
        api_version = [string]$Document.apiVersion
        kind = [string]$Document.kind
        items = @($items | ForEach-Object { ConvertTo-P005V122ResourceSummary -Resource $_ })
    }
}

function ConvertTo-P005V122ResourceSummary {
    param([Parameter(Mandatory)][object]$Resource)

    $podSpec = $null
    if ($Resource.kind -eq 'Pod') { $podSpec = $Resource.spec }
    elseif ($Resource.kind -eq 'Deployment') { $podSpec = $Resource.spec.template.spec }

    $images = @()
    $secretRefs = @()
    if ($null -ne $podSpec) {
        foreach ($container in @($podSpec.initContainers) + @($podSpec.containers)) {
            if ($null -ne $container -and -not [string]::IsNullOrWhiteSpace([string]$container.image)) { $images += [string]$container.image }
            foreach ($env in @($container.env)) {
                if ($null -ne $env.valueFrom.secretKeyRef.name) { $secretRefs += [string]$env.valueFrom.secretKeyRef.name }
            }
            foreach ($envFrom in @($container.envFrom)) {
                if ($null -ne $envFrom.secretRef.name) { $secretRefs += [string]$envFrom.secretRef.name }
            }
        }
        foreach ($volume in @($podSpec.volumes)) {
            if ($null -ne $volume.secret.secretName) { $secretRefs += [string]$volume.secret.secretName }
        }
        foreach ($pullSecret in @($podSpec.imagePullSecrets)) {
            if ($null -ne $pullSecret.name) { $secretRefs += [string]$pullSecret.name }
        }
    }

    return [ordered]@{
        metadata = [ordered]@{
            name = [string]$Resource.metadata.name
            namespace = [string]$Resource.metadata.namespace
            labels = ConvertTo-P005V122SafeEvidence -Value $Resource.metadata.labels
        }
        status = ConvertTo-P005V122SafeEvidence -Value $Resource.status
        images = @($images | Sort-Object -Unique)
        secret_ref_names = @($secretRefs | Sort-Object -Unique)
    }
}

function ConvertTo-P005V122SafeEvidence {
    param([AllowNull()][object]$Value)

    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        $result = [ordered]@{}
        foreach ($key in $Value.Keys) {
            if ([string]$key -match '(?i)(token|password|secret|authorization|credential)') { $result[$key] = '[REDACTED]'; continue }
            $result[$key] = ConvertTo-P005V122SafeEvidence -Value $Value[$key]
        }
        return $result
    }
    if ($Value -is [System.Collections.IEnumerable] -and -not ($Value -is [string])) {
        return @($Value | ForEach-Object { ConvertTo-P005V122SafeEvidence -Value $_ })
    }
    if ($Value -is [System.Management.Automation.PSCustomObject]) {
        $result = [ordered]@{}
        foreach ($property in $Value.PSObject.Properties) {
            if ($property.Name -match '(?i)(token|password|secret|authorization|credential)') { $result[$property.Name] = '[REDACTED]'; continue }
            $result[$property.Name] = ConvertTo-P005V122SafeEvidence -Value $property.Value
        }
        return $result
    }
    if ($Value -is [string] -and $Value -match '(?i)(?:bearer\s+|(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?|[a-z0-9-]+(?:\.[a-z0-9-]+)*\.svc\.cluster\.local(?::\d+)?|(?:[a-zA-Z]:\\|/home/|/etc/|/var/))') {
        return '[REDACTED]'
    }
    return $Value
}

function Start-P005V122MatrixPortForward {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$Kubeconfig,
        [Parameter(Mandatory)][string]$RuntimeRoot,
        [Parameter(Mandatory)][int]$LocalPort,
        [string]$Namespace = 'haowork-public',
        [Parameter(Mandatory)][string]$Service
    )

    $workerPath = Join-Path $RuntimeRoot 'matrix-port-forward.ps1'
    @'
param(
    [Parameter(Mandatory)][string]$Kubectl,
    [Parameter(Mandatory)][string]$Kubeconfig,
    [Parameter(Mandatory)][int]$LocalPort,
    [Parameter(Mandatory)][string]$Namespace,
    [Parameter(Mandatory)][string]$Service
)
$ErrorActionPreference = 'Continue'
$env:KUBECONFIG = $Kubeconfig
while ($true) {
    & $Kubectl port-forward -n $Namespace service/$Service "$LocalPort`:6167"
    Start-Sleep -Seconds 1
}
'@ | Set-Content -LiteralPath $workerPath -Encoding utf8
    $forwardArguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $workerPath + '" -Kubectl "' + $Kubectl + '" -Kubeconfig "' + $Kubeconfig + '" -LocalPort ' + $LocalPort + ' -Namespace "' + $Namespace + '" -Service "' + $Service + '"'
    $process = Start-Process -FilePath powershell.exe -ArgumentList $forwardArguments -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $RuntimeRoot 'matrix-port-forward.out.log') -RedirectStandardError (Join-Path $RuntimeRoot 'matrix-port-forward.err.log')
    return [pscustomobject]@{ Process = $process; WorkerPath = $workerPath }
}

function Assert-P005V122EvidenceDocument {
    param(
        [Parameter(Mandatory)][string]$Path,
        [switch]$AllowPartialEvidence
    )

    try {
        $evidence = Get-Content -LiteralPath $Path -Raw -Encoding utf8 | ConvertFrom-Json
    } catch {
        throw 'BLOCKED_CLUSTER_EVIDENCE_JSON'
    }
    if ([int]$evidence.schema_version -ne 1) { throw 'BLOCKED_CLUSTER_EVIDENCE_SCHEMA' }
    $allowed = @('schema_version', 'baseline', 'topology', 'skills', 'data_path', 'network_policy', 'restart')
    foreach ($property in $evidence.PSObject.Properties.Name) {
        if ($property -notin $allowed) { throw 'BLOCKED_CLUSTER_EVIDENCE_ADDITIONAL_PROPERTY' }
    }
    $requiredProperties = if ($AllowPartialEvidence) {
        @('baseline', 'topology')
    } else {
        @('baseline', 'topology', 'skills', 'data_path', 'network_policy', 'restart')
    }
    foreach ($property in $requiredProperties) {
        if ($null -eq $evidence.$property) { throw "BLOCKED_CLUSTER_EVIDENCE_$($property.ToUpperInvariant())" }
    }
    Assert-P005V122ExactProperties -Value $evidence.baseline -Allowed @('cluster_name', 'contract', 'crd_kinds', 'zones') -Required @('cluster_name', 'contract', 'crd_kinds', 'zones') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_BASELINE_ADDITIONAL_PROPERTY'
    Assert-P005V122ExactProperties -Value $evidence.baseline.contract -Allowed @('tag', 'commit', 'chart_version') -Required @('tag', 'commit', 'chart_version') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_CONTRACT_ADDITIONAL_PROPERTY'
    Assert-P005V122StringProperty -Value $evidence.baseline -Name 'cluster_name' -Pattern '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_CLUSTER_NAME'
    $crdKinds = @($evidence.baseline.crd_kinds)
    if ($crdKinds.Count -ne 4 -or (($crdKinds | Sort-Object -Unique) -join ',') -cne 'Human,Manager,Team,Worker') { throw 'BLOCKED_CLUSTER_EVIDENCE_CRD_KINDS' }
    $zones = @($evidence.baseline.zones)
    if ($zones.Count -ne 2) { throw 'BLOCKED_CLUSTER_EVIDENCE_ZONES' }
    $zoneNames = @()
    foreach ($zone in $zones) {
        Assert-P005V122ExactProperties -Value $zone -Allowed @('name', 'namespace', 'release') -Required @('name', 'namespace', 'release') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ZONE_ADDITIONAL_PROPERTY'
        $zoneName = [string]$zone.name
        if ($zoneName -notin @('public', 'internal') -or [string]$zone.namespace -cne "haowork-$zoneName" -or [string]$zone.release -cne "haowork-$zoneName-agentteams") { throw 'BLOCKED_CLUSTER_EVIDENCE_ZONE'
        }
        $zoneNames += $zoneName
    }
    if (($zoneNames | Sort-Object -Unique) -join ',' -cne 'internal,public') { throw 'BLOCKED_CLUSTER_EVIDENCE_ZONES' }
    Assert-P005V122ExactProperties -Value $evidence.topology -Allowed @('team_name', 'room_kind', 'agent_roles', 'zone') -Required @('team_name', 'room_kind', 'agent_roles', 'zone') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_TOPOLOGY_ADDITIONAL_PROPERTY'
    Assert-P005V122StringProperty -Value $evidence.topology -Name 'team_name' -Pattern '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_TEAM_NAME'
    if ([string]$evidence.topology.room_kind -cne 'leader_dm') { throw 'BLOCKED_CLUSTER_EVIDENCE_TOPOLOGY_ROOM_KIND' }
    if ([string]$evidence.topology.zone -notin @('public', 'internal')) { throw 'BLOCKED_CLUSTER_EVIDENCE_TOPOLOGY_ZONE' }
    $agentRoles = @($evidence.topology.agent_roles)
    if ($evidence.topology.agent_roles -is [string] -or $agentRoles.Count -ne 5 -or (($agentRoles | Sort-Object -Unique) -join ',') -cne 'build,delivery_leader,manager,research,verify') { throw 'BLOCKED_CLUSTER_EVIDENCE_AGENT_ROLES' }
    foreach ($role in @($evidence.topology.agent_roles)) {
        if ([string]$role -notin @('manager', 'delivery_leader', 'research', 'build', 'verify')) { throw 'BLOCKED_CLUSTER_EVIDENCE_AGENT_ROLE' }
    }
    if ($null -ne $evidence.skills) {
        Assert-P005V122ExactProperties -Value $evidence.skills -Allowed @('research', 'build', 'verify') -Required @('research', 'build', 'verify') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_SKILLS_ADDITIONAL_PROPERTY'
        Assert-P005V122StringArray -Value $evidence.skills.research -Allowed @('context', 'history', 'advisory', 'mirror') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_RESEARCH_SKILLS'
        Assert-P005V122StringArray -Value $evidence.skills.build -Allowed @('context', 'record', 'history', 'patch') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_BUILD_SKILLS'
        Assert-P005V122StringArray -Value $evidence.skills.verify -Allowed @('history', 'verify', 'audit') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_VERIFY_SKILLS'
    }
    if ($null -ne $evidence.data_path) {
        Assert-P005V122ExactProperties -Value $evidence.data_path.assignment -Allowed @('assignment_id', 'state') -Required @('assignment_id', 'state') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ASSIGNMENT_ADDITIONAL_PROPERTY'
        Assert-P005V122ExactProperties -Value $evidence.data_path.matrix -Allowed @('dispatch_event_id', 'room_kind') -Required @('dispatch_event_id', 'room_kind') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_ADDITIONAL_PROPERTY'
        Assert-P005V122ExactProperties -Value $evidence.data_path.receipt -Allowed @('receipt_id', 'report_sha256', 'leader_function', 'binding_revision') -Required @('receipt_id', 'report_sha256', 'leader_function', 'binding_revision') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_RECEIPT_ADDITIONAL_PROPERTY'
        Assert-P005V122ExactProperties -Value $evidence.data_path.artifact -Allowed @('uri', 'sha256', 'size', 'environment_id') -Required @('uri', 'sha256', 'size', 'environment_id') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_ADDITIONAL_PROPERTY'
        Assert-P005V122ExactProperties -Value $evidence.data_path.mcp -Allowed @('server_name', 'consumer_kind', 'trace_sha256') -Required @('server_name', 'consumer_kind', 'trace_sha256') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MCP_ADDITIONAL_PROPERTY'
    }
    if ([string]$evidence.baseline.contract.tag -cne 'v1.2.2' -or
        [string]$evidence.baseline.contract.commit -cne '849182af8e017168a5a200a87b1062142caf462d' -or
        [string]$evidence.baseline.contract.chart_version -cne '1.1.1') {
        throw 'BLOCKED_CLUSTER_EVIDENCE_CONTRACT'
    }
    if ($null -ne $evidence.network_policy) {
        if ([string]$evidence.network_policy.public_to_internal -cne 'denied' -or
            [string]$evidence.network_policy.internal_to_public -cne 'denied') {
            throw 'BLOCKED_CLUSTER_EVIDENCE_NETWORK_POLICY'
        }
        Assert-P005V122ExactProperties -Value $evidence.network_policy -Allowed @('public_to_internal', 'internal_to_public') -Required @('public_to_internal', 'internal_to_public') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_NETWORK_POLICY_ADDITIONAL_PROPERTY'
    }
    if ($null -ne $evidence.data_path) {
        Assert-P005V122StringProperty -Value $evidence.data_path.assignment -Name 'assignment_id' -Pattern '^ASN-[0-9a-f]{32}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ASSIGNMENT_ID'
        if ([string]$evidence.data_path.assignment.state -cne 'REPORT_SUBMITTED') { throw 'BLOCKED_CLUSTER_EVIDENCE_ASSIGNMENT_STATE' }
        Assert-P005V122StringProperty -Value $evidence.data_path.matrix -Name 'dispatch_event_id' -Pattern '^\$\S+$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_EVENT_ID'
        if (([string]$evidence.data_path.matrix.dispatch_event_id).Length -gt 255) { throw 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_EVENT_ID' }
        if ([string]$evidence.data_path.matrix.room_kind -cne 'leader_dm') { throw 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_ROOM_KIND' }
        Assert-P005V122StringProperty -Value $evidence.data_path.receipt -Name 'receipt_id' -Pattern '^ERC-[A-Za-z0-9][A-Za-z0-9._:-]{0,123}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_RECEIPT_ID'
        Assert-P005V122StringProperty -Value $evidence.data_path.receipt -Name 'report_sha256' -Pattern '^[0-9a-f]{64}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_REPORT_SHA256'
        if ([string]$evidence.data_path.receipt.leader_function -cne 'delivery_leader') { throw 'BLOCKED_CLUSTER_EVIDENCE_LEADER_FUNCTION' }
        Assert-P005V122NonNegativeIntegerProperty -Value $evidence.data_path.receipt -Name 'binding_revision' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_BINDING_REVISION'
        if ([int]$evidence.data_path.receipt.binding_revision -lt 1) { throw 'BLOCKED_CLUSTER_EVIDENCE_BINDING_REVISION' }
        if (-not (Test-P005V122S3ArtifactURI -Value $evidence.data_path.artifact.uri)) { throw 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_URI' }
        Assert-P005V122StringProperty -Value $evidence.data_path.artifact -Name 'sha256' -Pattern '^[0-9a-f]{64}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_SHA256'
        Assert-P005V122NonNegativeIntegerProperty -Value $evidence.data_path.artifact -Name 'size' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_SIZE'
        Assert-P005V122StringProperty -Value $evidence.data_path.artifact -Name 'environment_id' -Pattern '^[a-z][a-z0-9-]{0,62}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_ENVIRONMENT'
        Assert-P005V122StringProperty -Value $evidence.data_path.mcp -Name 'server_name' -Pattern '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MCP_NAME'
        if ([string]$evidence.data_path.mcp.consumer_kind -cne 'agentteams_worker') { throw 'BLOCKED_CLUSTER_EVIDENCE_CONSUMER_KIND' }
        Assert-P005V122StringProperty -Value $evidence.data_path.mcp -Name 'trace_sha256' -Pattern '^[0-9a-f]{64}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_TRACE_CORE'
    }
    if ($null -ne $evidence.restart) {
        Assert-P005V122ExactProperties -Value $evidence.restart -Allowed @('controller', 'start_new_state', 'receipt_id') -Required @('controller', 'start_new_state', 'receipt_id') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_RESTART_ADDITIONAL_PROPERTY'
        if ([string]$evidence.restart.controller -cne 'core_bridge' -or [string]$evidence.restart.start_new_state -cne 'FRESH_RUN') { throw 'BLOCKED_CLUSTER_EVIDENCE_RESTART' }
        Assert-P005V122StringProperty -Value $evidence.restart -Name 'receipt_id' -Pattern '^ERC-[A-Za-z0-9][A-Za-z0-9._:-]{0,123}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_RESTART_RECEIPT'
    }
    Assert-P005V122SafeEvidenceValue -Value $evidence -Path '$'
    return $evidence
}

function Assert-P005V122ExactProperties {
    param(
        [Parameter(Mandatory)][object]$Value,
        [Parameter(Mandatory)][string[]]$Allowed,
        [Parameter(Mandatory)][string[]]$Required,
        [Parameter(Mandatory)][string]$Blocker
    )

    if ($null -eq $Value) { throw $Blocker }
    foreach ($name in $Value.PSObject.Properties.Name) {
        if ($name -notin $Allowed) { throw $Blocker }
    }
    foreach ($name in $Required) {
        if ($null -eq $Value.PSObject.Properties[$name]) { throw $Blocker }
    }
}

function Assert-P005V122StringProperty {
    param(
        [Parameter(Mandatory)][object]$Value,
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Pattern,
        [Parameter(Mandatory)][string]$Blocker
    )

    $property = $Value.PSObject.Properties[$Name]
    if ($null -eq $property -or $property.Value -isnot [string] -or [string]::IsNullOrWhiteSpace($property.Value) -or $property.Value -notmatch $Pattern) { throw $Blocker }
}

function Assert-P005V122NonNegativeIntegerProperty {
    param(
        [Parameter(Mandatory)][object]$Value,
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Blocker
    )

    $property = $Value.PSObject.Properties[$Name]
    if ($null -eq $property -or ($property.Value -isnot [int] -and $property.Value -isnot [long]) -or $property.Value -lt 0) { throw $Blocker }
}

function Assert-P005V122StringArray {
    param(
        [Parameter(Mandatory)][object]$Value,
        [Parameter(Mandatory)][string[]]$Allowed,
        [Parameter(Mandatory)][string]$Blocker
    )

    if ($Value -is [string]) { throw $Blocker }
    $items = @($Value)
    if ($items.Count -lt 1 -or $items.Count -gt 8 -or ($items | Sort-Object -Unique).Count -ne $items.Count) { throw $Blocker }
    foreach ($item in $items) {
        if ($item -isnot [string] -or ([string]$item) -notin $Allowed) { throw $Blocker }
    }
}

function Test-P005V122S3ArtifactURI {
    param([AllowNull()][object]$Value)

    if ($Value -isnot [string] -or [string]::IsNullOrWhiteSpace($Value) -or $Value.Length -gt 2048) { return $false }
    $raw = [string]$Value
    if (-not $raw.StartsWith('s3://', [StringComparison]::Ordinal) -or $raw.Contains('%')) { return $false }
    $pathStart = $raw.IndexOf('/', 5)
    if ($pathStart -le 5 -or $pathStart -eq $raw.Length - 1) { return $false }
    $authority = $raw.Substring(5, $pathStart - 5)
    if ($authority.Contains('@') -or $authority.Contains(':') -or $authority.Contains('..') -or $authority -notmatch '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$') { return $false }
    $rawPath = $raw.Substring($pathStart + 1)
    if ($rawPath -notmatch '^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$') { return $false }
    $uri = $null
    if (-not [Uri]::TryCreate($raw, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -cne 's3' -or -not [string]::IsNullOrEmpty($uri.UserInfo) -or $uri.Host -cne $authority -or -not $uri.IsDefaultPort -or $uri.OriginalString.Contains('%') -or $uri.AbsolutePath.Contains('%')) { return $false }
    foreach ($segment in $rawPath.Split('/')) {
        if ($segment -in @('.', '..')) { return $false }
    }
    return $true
}

function Assert-P005V122SafeEvidenceValue {
    param([AllowNull()][object]$Value, [Parameter(Mandatory)][string]$Path)

    if ($null -eq $Value) { return }
    if ($Value -is [System.Collections.IDictionary]) {
        foreach ($key in $Value.Keys) {
            if ([string]$key -match '(?i)(token|password|secret|authorization|credential|api[_-]?key|correlation[_-]?id|matrix[_-]?body|raw[_-]?body|private[_-]?path|client[_-]?ip|host[_-]?credential)') { throw 'BLOCKED_CLUSTER_EVIDENCE_SENSITIVE_DATA' }
            Assert-P005V122SafeEvidenceValue -Value $Value[$key] -Path "$Path.$key"
        }
        return
    }
    if ($Value -is [System.Management.Automation.PSCustomObject]) {
        foreach ($property in $Value.PSObject.Properties) {
            if ($property.Name -match '(?i)(token|password|secret|authorization|credential|api[_-]?key|correlation[_-]?id|matrix[_-]?body|raw[_-]?body|private[_-]?path|client[_-]?ip|host[_-]?credential)') { throw 'BLOCKED_CLUSTER_EVIDENCE_SENSITIVE_DATA' }
            Assert-P005V122SafeEvidenceValue -Value $property.Value -Path "$Path.$($property.Name)"
        }
        return
    }
    if ($Value -is [System.Collections.IEnumerable] -and -not ($Value -is [string])) {
        $index = 0
        foreach ($item in $Value) {
            Assert-P005V122SafeEvidenceValue -Value $item -Path "$Path[$index]"
            $index++
        }
        return
    }
    if ($Value -is [string]) {
        if ($Value -ceq '[REDACTED]') { return }
        if ($Value -match '(?i)(?:bearer\s+\S+|\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b|\b(?:token|password|api[_-]?key|key|secret|credential)\s*[:=]\s*\S+|\bsk-[A-Za-z0-9_-]{12,}|\bgh[pousr]_[A-Za-z0-9]{20,}|\bAKIA[0-9A-Z]{16}\b|[a-z][a-z0-9+.-]*://[^\s/]*@|[a-zA-Z]:[\\/]|/(?:home|root|etc|var|Users)/|\b(?:10(?:\.\d{1,3}){3}|127(?:\.\d{1,3}){3}|169\.254(?:\.\d{1,3}){2}|192\.168(?:\.\d{1,3}){2}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2})\b|\.svc\.cluster\.local\b)') {
            throw 'BLOCKED_CLUSTER_EVIDENCE_SENSITIVE_DATA'
        }
    }
}

function Write-P005V122EvidenceManifest {
    param(
        [Parameter(Mandatory)][string]$EvidenceRoot,
        [Parameter(Mandatory)][string]$OutputPath
    )

    $files = Get-ChildItem -LiteralPath $EvidenceRoot -File | Where-Object { $_.FullName -ine $OutputPath } | Sort-Object Name
    $entries = foreach ($file in $files) {
        [ordered]@{
            path = $file.Name
            size = $file.Length
            sha256 = Get-P005V122SHA256 -Path $file.FullName
        }
    }
    [ordered]@{ schema_version = 1; files = @($entries) } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $OutputPath -Encoding utf8
}

function Get-P005V122SHA256 {
    param([Parameter(Mandatory)][string]$Path)

    $stream = [IO.File]::OpenRead($Path)
    try {
        $algorithm = [Security.Cryptography.SHA256]::Create()
        try {
            $hash = $algorithm.ComputeHash($stream)
            return (($hash | ForEach-Object { $_.ToString('x2') }) -join '')
        } finally {
            $algorithm.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

$ClusterName = Assert-P005V122ClusterName -Name $ClusterName
$worktreeRoot = Split-Path -Parent $PSScriptRoot
$commonPath = Join-Path $PSScriptRoot 'p0-05-v122-common.ps1'
. $commonPath
$contract = Get-P005V122OfficialContract -ContractPath (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\upstream.lock.json')
$lockedManagerImage = Get-P005V122LockedImageReference -Contract $contract -Name 'manager'
$repoRoot = Resolve-P005V122RepoRoot -WorktreeRoot $worktreeRoot
if (-not $repoRoot.StartsWith('E:\', [StringComparison]::OrdinalIgnoreCase)) { throw 'BLOCKED_CACHE_DRIVE' }
$cacheRoot = Join-Path $repoRoot '.haowork\cache'
$evidenceRoot = Join-Path $worktreeRoot '.haowork\cache\evidence\p0-05-v1.2.2\cluster'
$runtimeRoot = Join-Path $cacheRoot 'runtime\p0-05-v1.2.2\cluster-e2e'
New-Item -ItemType Directory -Force $cacheRoot, $evidenceRoot, $runtimeRoot | Out-Null
$env:TEMP = Join-Path $cacheRoot 'tmp'
$env:TMP = $env:TEMP
$env:GOCACHE = Join-Path $cacheRoot 'go'
$env:GOMODCACHE = Join-Path $cacheRoot 'gomod'
foreach ($directory in @($env:TEMP, $env:GOCACHE, $env:GOMODCACHE)) { New-Item -ItemType Directory -Force $directory | Out-Null }
$env:KUBECONFIG = Join-Path $cacheRoot "kind\$ClusterName.kubeconfig"
$localEnvironmentPath = [string]$env:HAOWORK_AGENTTEAMS_ENV_FILE
if ([string]::IsNullOrWhiteSpace($localEnvironmentPath)) {
    $localEnvironmentPath = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\.env.local'
}
if (-not (Test-Path -LiteralPath $localEnvironmentPath -PathType Leaf)) { throw 'BLOCKED_RUNNER_LOCAL_ENV' }

if (-not (Test-Path -LiteralPath $env:KUBECONFIG -PathType Leaf)) {
    throw 'BLOCKED_KUBECONFIG'
}

$kind = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kind'
$kubectl = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kubectl'
$go = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'go'
$environmentLoader = Join-Path $PSScriptRoot 'p0-05-v122-env.ps1'
if (-not (Test-Path -LiteralPath $environmentLoader -PathType Leaf)) { throw 'BLOCKED_LOCAL_ENV_LOADER' }
. $environmentLoader
Import-P005V122LocalEnvironment -Path $localEnvironmentPath
$internalEnvironmentFields = @(
    'HAOWORK_P005_INTERNAL_LLM_PROVIDER', 'HAOWORK_P005_INTERNAL_LLM_BASE_URL',
    'HAOWORK_P005_INTERNAL_LLM_API_KEY', 'HAOWORK_P005_INTERNAL_LLM_MODEL',
    'HAOWORK_P005_INTERNAL_EGRESS_CIDRS'
)
$publicEnvironmentFields = @(
    'HAOWORK_P005_PUBLIC_LLM_PROVIDER', 'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
    'HAOWORK_P005_PUBLIC_LLM_API_KEY', 'HAOWORK_P005_PUBLIC_LLM_MODEL',
    'HAOWORK_P005_PUBLIC_EGRESS_CIDRS'
)
foreach ($name in $publicEnvironmentFields) {
    $value = [Environment]::GetEnvironmentVariable($name, 'Process')
    if ([string]::IsNullOrWhiteSpace($value) -or $value -match '[\r\n]') { throw 'BLOCKED_RUNNER_LOCAL_ENV' }
}
foreach ($name in $internalEnvironmentFields) {
    $value = [Environment]::GetEnvironmentVariable($name, 'Process')
    if ([string]::IsNullOrWhiteSpace($value) -or $value -match '[\r\n]') { throw 'BLOCKED_INTERNAL_ENV' }
}
$publicModel = [string]$env:HAOWORK_P005_PUBLIC_LLM_MODEL
if ([string]::IsNullOrWhiteSpace($publicModel) -or $publicModel -match '[\r\n]') { throw 'BLOCKED_CLUSTER_MODEL' }
$env:HAOWORK_P005_CLUSTER_EXECUTION_ID = 'E2E-' + [guid]::NewGuid().ToString('N').Substring(0, 16)
$env:HAOWORK_P005_CLUSTER_MISSION_ID = 'MSN-P005-V122-REAL'
$env:HAOWORK_P005_CLUSTER_PUBLIC_MISSION_ID = $env:HAOWORK_P005_CLUSTER_MISSION_ID
$env:HAOWORK_P005_CLUSTER_INTERNAL_MISSION_ID = 'MSN-P005-V122-INTERNAL'
$env:HAOWORK_P005_CLUSTER_CONTROLLER_NAME = 'haowork-public-agentteams-controller'
$env:HAOWORK_P005_CLUSTER_PUBLIC_CONTROLLER_NAME = $env:HAOWORK_P005_CLUSTER_CONTROLLER_NAME
$env:HAOWORK_P005_CLUSTER_INTERNAL_CONTROLLER_NAME = 'haowork-internal-agentteams-controller'
$env:HAOWORK_P005_CLUSTER_MODEL = $publicModel
$env:HAOWORK_P005_CLUSTER_PUBLIC_MODEL = $publicModel
$internalModel = [string]$env:HAOWORK_P005_INTERNAL_LLM_MODEL
if ([string]::IsNullOrWhiteSpace($internalModel) -or $internalModel -match '[\r\n]') { throw 'BLOCKED_INTERNAL_ENV' }
$env:HAOWORK_P005_CLUSTER_INTERNAL_MODEL = $internalModel
$env:HAOWORK_P005_CLUSTER_MANAGER_RUNTIME = 'openclaw'
$env:HAOWORK_P005_CLUSTER_WORKER_RUNTIME = 'openclaw'
$env:HAOWORK_P005_CLUSTER_RECEIPT_TOOL_NAME = 'submit_execution_report'
$env:HAOWORK_P005_CLUSTER_EXECUTION_RECEIPT = 'execution_receipt'
$managerImage = ([string](& $kubectl get pod haowork-public-agentteams-manager -n haowork-public -o "jsonpath={.spec.containers[?(@.name=='worker')].image}")).Trim()
if ($LASTEXITCODE -ne 0 -or $managerImage -cne $lockedManagerImage) { throw 'BLOCKED_CLUSTER_MANAGER_IMAGE' }
$env:HAOWORK_P005_CLUSTER_MANAGER_IMAGE = $managerImage
$env:HAOWORK_P005_CLUSTER_MCP_URL = 'http://aigw-local.agentteams.io:8080/mcp-servers/haowork-mcp'
$env:HAOWORK_P005_CLUSTER_MCP_SERVER_NAME = 'haowork-mcp'
$env:HAOWORK_P005_CLUSTER_MCP_TRANSPORT = 'http'
$env:HAOWORK_P005_CLUSTER_HUMAN_NAME = 'owner'
$env:HAOWORK_P005_CLUSTER_PUBLIC_MCP_URL = $env:HAOWORK_P005_CLUSTER_MCP_URL
$env:HAOWORK_P005_CLUSTER_INTERNAL_MCP_URL = 'http://aigw-local.agentteams.io:8080/mcp-servers/haowork-mcp'
$env:HAOWORK_P005_CLUSTER_PUBLIC_MCP_SERVER_NAME = $env:HAOWORK_P005_CLUSTER_MCP_SERVER_NAME
$env:HAOWORK_P005_CLUSTER_INTERNAL_MCP_SERVER_NAME = $env:HAOWORK_P005_CLUSTER_MCP_SERVER_NAME
$env:HAOWORK_P005_CLUSTER_PUBLIC_MCP_TRANSPORT = $env:HAOWORK_P005_CLUSTER_MCP_TRANSPORT
$env:HAOWORK_P005_CLUSTER_INTERNAL_MCP_TRANSPORT = $env:HAOWORK_P005_CLUSTER_MCP_TRANSPORT
$env:HAOWORK_P005_CLUSTER_PUBLIC_HUMAN_NAME = $env:HAOWORK_P005_CLUSTER_HUMAN_NAME
$env:HAOWORK_P005_CLUSTER_INTERNAL_HUMAN_NAME = $env:HAOWORK_P005_CLUSTER_HUMAN_NAME
$internalManagerImage = ([string](& $kubectl get pod haowork-internal-agentteams-manager -n haowork-internal -o "jsonpath={.spec.containers[?(@.name=='worker')].image}")).Trim()
if ($LASTEXITCODE -ne 0 -or $internalManagerImage -cne $lockedManagerImage) { throw 'BLOCKED_INTERNAL_MANAGER_IMAGE' }
$env:HAOWORK_P005_CLUSTER_INTERNAL_MANAGER_IMAGE = $internalManagerImage
$env:HAOWORK_P005_CLUSTER_PUBLIC_MANAGER_IMAGE = $env:HAOWORK_P005_CLUSTER_MANAGER_IMAGE
$clusters = @(& $kind get clusters)
if ($LASTEXITCODE -ne 0 -or $clusters -notcontains $ClusterName) { throw 'BLOCKED_KIND_CLUSTER' }
$context = (& $kubectl config current-context).Trim()
if ($LASTEXITCODE -ne 0 -or $context -cne "kind-$ClusterName") { throw 'BLOCKED_KUBERNETES_CONTEXT' }

$schemaPath = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\cluster-evidence.schema.json'
try { Get-Content -LiteralPath $schemaPath -Raw -Encoding utf8 | ConvertFrom-Json | Out-Null } catch { throw 'BLOCKED_CLUSTER_EVIDENCE_SCHEMA_FILE' }
$clusterEvidencePath = Join-Path $evidenceRoot 'cluster-evidence.json'
if (Test-Path -LiteralPath $clusterEvidencePath) { Remove-Item -LiteralPath $clusterEvidencePath -Force }

$env:HAOWORK_P005_CLUSTER_NAME = $ClusterName
$env:HAOWORK_P005_CLUSTER_EVIDENCE_DIR = $evidenceRoot
$encodedToken = ([string](& $kubectl get secret haowork-core-bridge-runtime -n haowork-public -o 'jsonpath={.data.token}')).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($encodedToken)) { throw 'BLOCKED_HAOWORK_CORE_BRIDGE_TOKEN' }
$internalEncodedToken = ([string](& $kubectl get secret haowork-core-bridge-runtime -n haowork-internal -o 'jsonpath={.data.token}')).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($internalEncodedToken)) { throw 'BLOCKED_INTERNAL_CORE_BRIDGE_TOKEN' }
$env:HAOWORK_P005_CLUSTER_PUBLIC_CORE_BRIDGE_TOKEN = ConvertFrom-P005V122Base64 -Value $encodedToken
$env:HAOWORK_P005_CLUSTER_INTERNAL_CORE_BRIDGE_TOKEN = ConvertFrom-P005V122Base64 -Value $internalEncodedToken
$env:HAOWORK_P005_CLUSTER_CORE_BRIDGE_TOKEN = $env:HAOWORK_P005_CLUSTER_PUBLIC_CORE_BRIDGE_TOKEN
$coreBridgePort = Get-P005V122LoopbackPort
$internalCoreBridgePort = Get-P005V122LoopbackPort
$env:HAOWORK_P005_CLUSTER_PUBLIC_CORE_BRIDGE_READY_URL = "http://127.0.0.1:$coreBridgePort/ready"
$env:HAOWORK_P005_CLUSTER_INTERNAL_CORE_BRIDGE_READY_URL = "http://127.0.0.1:$internalCoreBridgePort/ready"
$env:HAOWORK_P005_CLUSTER_CORE_BRIDGE_READY_URL = $env:HAOWORK_P005_CLUSTER_PUBLIC_CORE_BRIDGE_READY_URL
$mcpGatewayPort = Get-P005V122LoopbackPort
$internalMcpGatewayPort = Get-P005V122LoopbackPort
$env:HAOWORK_P005_CLUSTER_PUBLIC_MCP_GATEWAY_URL = "http://127.0.0.1:$mcpGatewayPort/mcp-servers/haowork-mcp"
$env:HAOWORK_P005_CLUSTER_INTERNAL_MCP_GATEWAY_URL = "http://127.0.0.1:$internalMcpGatewayPort/mcp-servers/haowork-mcp"
$env:HAOWORK_P005_CLUSTER_MCP_GATEWAY_URL = $env:HAOWORK_P005_CLUSTER_PUBLIC_MCP_GATEWAY_URL
$matrixPort = Get-P005V122LoopbackPort
$internalMatrixPort = Get-P005V122LoopbackPort
$env:HAOWORK_P005_CLUSTER_PUBLIC_MATRIX_URL = "http://127.0.0.1:$matrixPort"
$env:HAOWORK_P005_CLUSTER_INTERNAL_MATRIX_URL = "http://127.0.0.1:$internalMatrixPort"
$env:HAOWORK_P005_CLUSTER_MATRIX_URL = $env:HAOWORK_P005_CLUSTER_PUBLIC_MATRIX_URL
$publicRuntimeRoot = Join-Path $runtimeRoot 'public'
$internalRuntimeRoot = Join-Path $runtimeRoot 'internal'
New-Item -ItemType Directory -Force $publicRuntimeRoot, $internalRuntimeRoot | Out-Null
$forward = $null
$internalForward = $null
$gatewayForward = $null
$internalGatewayForward = $null
$matrixForward = $null
$internalMatrixForward = $null
try {
    $forward = Start-P005V122CoreBridgePortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $publicRuntimeRoot -LocalPort $coreBridgePort -Namespace 'haowork-public' -Service 'haowork-core-bridge'
    $internalForward = Start-P005V122CoreBridgePortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $internalRuntimeRoot -LocalPort $internalCoreBridgePort -Namespace 'haowork-internal' -Service 'haowork-core-bridge'
    Wait-P005V122CoreBridgeReady -Token $env:HAOWORK_P005_CLUSTER_PUBLIC_CORE_BRIDGE_TOKEN -ReadyURL $env:HAOWORK_P005_CLUSTER_PUBLIC_CORE_BRIDGE_READY_URL
    Wait-P005V122CoreBridgeReady -Token $env:HAOWORK_P005_CLUSTER_INTERNAL_CORE_BRIDGE_TOKEN -ReadyURL $env:HAOWORK_P005_CLUSTER_INTERNAL_CORE_BRIDGE_READY_URL
    $gatewayForward = Start-P005V122MCPGatewayPortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $publicRuntimeRoot -LocalPort $mcpGatewayPort -Namespace 'haowork-public'
    $internalGatewayForward = Start-P005V122MCPGatewayPortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $internalRuntimeRoot -LocalPort $internalMcpGatewayPort -Namespace 'haowork-internal'
    $matrixForward = Start-P005V122MatrixPortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $publicRuntimeRoot -LocalPort $matrixPort -Namespace 'haowork-public' -Service 'haowork-public-agentteams-tuwunel'
    $internalMatrixForward = Start-P005V122MatrixPortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $internalRuntimeRoot -LocalPort $internalMatrixPort -Namespace 'haowork-internal' -Service 'haowork-internal-agentteams-tuwunel'
    Push-Location $worktreeRoot
    try {
        & $go test -tags agentteams_cluster_e2e ./internal/e2e -run $TestPattern -count=1 -timeout 30m
        if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CLUSTER_E2E' }
    } finally {
        Pop-Location
    }
} finally {
    Stop-P005V122CoreBridgePortForward -Forward $internalMatrixForward
    Stop-P005V122CoreBridgePortForward -Forward $matrixForward
    Stop-P005V122CoreBridgePortForward -Forward $internalGatewayForward
    Stop-P005V122CoreBridgePortForward -Forward $gatewayForward
    Stop-P005V122CoreBridgePortForward -Forward $internalForward
    Stop-P005V122CoreBridgePortForward -Forward $forward
}

$allowPartialEvidence = $TestPattern -cne 'TestP005V122'
Assert-P005V122EvidenceDocument -Path $clusterEvidencePath -AllowPartialEvidence:$allowPartialEvidence | Out-Null
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'crd', 'managers.agentteams.io', 'workers.agentteams.io', 'teams.agentteams.io', 'humans.agentteams.io', '-o', 'json') -Path (Join-Path $evidenceRoot 'crd.json') -Blocker 'BLOCKED_EVIDENCE_CRD'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'namespace', 'haowork-public', 'haowork-internal', '-o', 'json') -Path (Join-Path $evidenceRoot 'namespace.json') -Blocker 'BLOCKED_EVIDENCE_NAMESPACE'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'pods,deployments', '-n', 'haowork-public', '-o', 'json') -Path (Join-Path $evidenceRoot 'public-pod.json') -Blocker 'BLOCKED_EVIDENCE_PUBLIC_POD'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'pods,deployments', '-n', 'haowork-internal', '-o', 'json') -Path (Join-Path $evidenceRoot 'internal-pod.json') -Blocker 'BLOCKED_EVIDENCE_INTERNAL_POD'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'networkpolicy', '-n', 'haowork-public', '-o', 'json') -Path (Join-Path $evidenceRoot 'public-network-policy.json') -Blocker 'BLOCKED_EVIDENCE_PUBLIC_NETWORK_POLICY'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'networkpolicy', '-n', 'haowork-internal', '-o', 'json') -Path (Join-Path $evidenceRoot 'internal-network-policy.json') -Blocker 'BLOCKED_EVIDENCE_INTERNAL_NETWORK_POLICY'
Write-P005V122EvidenceManifest -EvidenceRoot $evidenceRoot -OutputPath (Join-Path $evidenceRoot 'sha256-manifest.json')
$evidenceMode = if ($allowPartialEvidence) { 'filtered' } else { 'complete' }
Write-Output "PASS P0-05 v1.2.2 $evidenceMode cluster E2E evidence written to $evidenceRoot"
