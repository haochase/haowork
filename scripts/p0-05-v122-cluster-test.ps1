[CmdletBinding()]
param(
    [string]$ClusterName = 'haowork-p005-v122'
)

$ErrorActionPreference = 'Stop'

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
        [Parameter(Mandatory)][int]$LocalPort
    )

    $workerPath = Join-Path $RuntimeRoot 'core-bridge-port-forward.ps1'
    @'
param(
    [Parameter(Mandatory)][string]$Kubectl,
    [Parameter(Mandatory)][string]$Kubeconfig,
    [Parameter(Mandatory)][int]$LocalPort
)
$ErrorActionPreference = 'Continue'
$env:KUBECONFIG = $Kubeconfig
while ($true) {
    & $Kubectl port-forward -n haowork-public service/haowork-core-bridge "$LocalPort`:8081"
    Start-Sleep -Seconds 1
}
'@ | Set-Content -LiteralPath $workerPath -Encoding utf8
    $forwardArguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $workerPath + '" -Kubectl "' + $Kubectl + '" -Kubeconfig "' + $Kubeconfig + '" -LocalPort ' + $LocalPort
    $process = Start-Process -FilePath powershell.exe -ArgumentList $forwardArguments -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $RuntimeRoot 'core-bridge-port-forward.out.log') -RedirectStandardError (Join-Path $RuntimeRoot 'core-bridge-port-forward.err.log')
    return [pscustomobject]@{ Process = $process; WorkerPath = $workerPath }
}

function Start-P005V122MCPGatewayPortForward {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$Kubeconfig,
        [Parameter(Mandatory)][string]$RuntimeRoot,
        [Parameter(Mandatory)][int]$LocalPort
    )

    $workerPath = Join-Path $RuntimeRoot 'mcp-gateway-port-forward.ps1'
    @'
param(
    [Parameter(Mandatory)][string]$Kubectl,
    [Parameter(Mandatory)][string]$Kubeconfig,
    [Parameter(Mandatory)][int]$LocalPort
)
$ErrorActionPreference = 'Continue'
$env:KUBECONFIG = $Kubeconfig
while ($true) {
    & $Kubectl port-forward -n haowork-public service/higress-gateway "$LocalPort`:80"
    Start-Sleep -Seconds 1
}
'@ | Set-Content -LiteralPath $workerPath -Encoding utf8
    $forwardArguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $workerPath + '" -Kubectl "' + $Kubectl + '" -Kubeconfig "' + $Kubeconfig + '" -LocalPort ' + $LocalPort
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
    return $Value
}

function Assert-P005V122EvidenceDocument {
    param([Parameter(Mandatory)][string]$Path)

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
    foreach ($property in @('baseline', 'topology', 'skills', 'data_path', 'network_policy', 'restart')) {
        if ($null -eq $evidence.$property) { throw "BLOCKED_CLUSTER_EVIDENCE_$($property.ToUpperInvariant())" }
    }
    Assert-P005V122ExactProperties -Value $evidence.data_path.matrix -Allowed @('event_id', 'sender_id', 'room_id', 'mission_id') -Required @('event_id', 'sender_id', 'room_id', 'mission_id') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_ADDITIONAL_PROPERTY'
    Assert-P005V122ExactProperties -Value $evidence.data_path.artifact -Allowed @('uri', 'sha256', 'size', 'environment_id', 's3_key') -Required @('uri', 'sha256', 'size', 'environment_id', 's3_key') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_ADDITIONAL_PROPERTY'
    Assert-P005V122ExactProperties -Value $evidence.data_path.mcp -Allowed @('consumer_name', 'route_name', 'server_name', 'trace_id', 'trace_sha256', 'core_history_sha256') -Required @('consumer_name', 'route_name', 'server_name', 'trace_id', 'trace_sha256', 'core_history_sha256') -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MCP_ADDITIONAL_PROPERTY'
    if ([string]$evidence.baseline.contract.tag -cne 'v1.2.2' -or
        [string]$evidence.baseline.contract.commit -cne '849182af8e017168a5a200a87b1062142caf462d' -or
        [string]$evidence.baseline.contract.chart_version -cne '1.1.1') {
        throw 'BLOCKED_CLUSTER_EVIDENCE_CONTRACT'
    }
    if ([string]$evidence.network_policy.public_to_internal -cne 'denied' -or
        [string]$evidence.network_policy.internal_to_public -cne 'denied') {
        throw 'BLOCKED_CLUSTER_EVIDENCE_NETWORK_POLICY'
    }
    Assert-P005V122StringProperty -Value $evidence.data_path.matrix -Name 'event_id' -Pattern '^\$\S+$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_EVENT_ID'
    Assert-P005V122StringProperty -Value $evidence.data_path.matrix -Name 'sender_id' -Pattern '^@[^\s:]+:.+$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_SENDER_ID'
    Assert-P005V122StringProperty -Value $evidence.data_path.matrix -Name 'room_id' -Pattern '^![^\s:]+:.+$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_ROOM_ID'
    Assert-P005V122StringProperty -Value $evidence.data_path.matrix -Name 'mission_id' -Pattern '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MATRIX_MISSION_ID'
    Assert-P005V122StringProperty -Value $evidence.data_path.artifact -Name 'uri' -Pattern '^s3://[^/]+/.+$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_URI'
    Assert-P005V122StringProperty -Value $evidence.data_path.artifact -Name 'sha256' -Pattern '^[0-9a-f]{64}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_SHA256'
    Assert-P005V122NonNegativeIntegerProperty -Value $evidence.data_path.artifact -Name 'size' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_SIZE'
    Assert-P005V122StringProperty -Value $evidence.data_path.artifact -Name 'environment_id' -Pattern '^[a-z][a-z0-9-]{0,62}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_ENVIRONMENT'
    Assert-P005V122StringProperty -Value $evidence.data_path.artifact -Name 's3_key' -Pattern '^[^/].*$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_ARTIFACT_KEY'
    foreach ($name in @('consumer_name', 'server_name')) {
        Assert-P005V122StringProperty -Value $evidence.data_path.mcp -Name $name -Pattern '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MCP_NAME'
    }
    Assert-P005V122StringProperty -Value $evidence.data_path.mcp -Name 'route_name' -Pattern '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.internal$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_MCP_NAME'
    Assert-P005V122StringProperty -Value $evidence.data_path.mcp -Name 'trace_id' -Pattern '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_TRACE_CORE'
    foreach ($name in @('trace_sha256', 'core_history_sha256')) {
        Assert-P005V122StringProperty -Value $evidence.data_path.mcp -Name $name -Pattern '^[0-9a-f]{64}$' -Blocker 'BLOCKED_CLUSTER_EVIDENCE_TRACE_CORE'
    }
    if ([string]$evidence.restart.opaque_cursor_before -eq '' -or [string]$evidence.restart.opaque_cursor_after -eq '' -or [string]$evidence.restart.new_event_id -eq '') { throw 'BLOCKED_CLUSTER_EVIDENCE_RESTART' }
    $serialized = $evidence | ConvertTo-Json -Depth 32
    if ($serialized -match '(?i)(token|password|secret|authorization|credential)') { throw 'BLOCKED_CLUSTER_EVIDENCE_SENSITIVE_DATA' }
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
if (-not (Test-Path -LiteralPath $env:KUBECONFIG -PathType Leaf)) { throw 'BLOCKED_KUBECONFIG' }

$kind = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kind'
$kubectl = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kubectl'
$go = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'go'
$environmentLoader = Join-Path $PSScriptRoot 'p0-05-v122-env.ps1'
if (-not (Test-Path -LiteralPath $environmentLoader -PathType Leaf)) { throw 'BLOCKED_LOCAL_ENV_LOADER' }
. $environmentLoader
Import-P005V122LocalEnvironment -Path (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\.env.local')
$publicModel = [string]$env:HAOWORK_P005_PUBLIC_LLM_MODEL
if ([string]::IsNullOrWhiteSpace($publicModel) -or $publicModel -match '[\r\n]') { throw 'BLOCKED_CLUSTER_MODEL' }
$env:HAOWORK_P005_CLUSTER_EXECUTION_ID = 'E2E-' + [guid]::NewGuid().ToString('N').Substring(0, 16)
$env:HAOWORK_P005_CLUSTER_MISSION_ID = 'MSN-P005-V122-REAL'
$env:HAOWORK_P005_CLUSTER_CONTROLLER_NAME = 'haowork-public-agentteams-controller'
$env:HAOWORK_P005_CLUSTER_MODEL = $publicModel
$env:HAOWORK_P005_CLUSTER_MANAGER_RUNTIME = 'openclaw'
$env:HAOWORK_P005_CLUSTER_WORKER_RUNTIME = 'openclaw'
$managerImage = ([string](& $kubectl get pod haowork-public-agentteams-manager -n haowork-public -o "jsonpath={.spec.containers[?(@.name=='worker')].image}")).Trim()
if ($LASTEXITCODE -ne 0 -or $managerImage -cne $lockedManagerImage) { throw 'BLOCKED_CLUSTER_MANAGER_IMAGE' }
$env:HAOWORK_P005_CLUSTER_MANAGER_IMAGE = $managerImage
$env:HAOWORK_P005_CLUSTER_MCP_URL = 'http://aigw-local.agentteams.io:8080/mcp-servers/haowork-mcp'
$env:HAOWORK_P005_CLUSTER_MCP_SERVER_NAME = 'haowork-mcp'
$env:HAOWORK_P005_CLUSTER_MCP_TRANSPORT = 'http'
$env:HAOWORK_P005_CLUSTER_HUMAN_NAME = 'owner'
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
$env:HAOWORK_P005_CLUSTER_CORE_BRIDGE_TOKEN = ConvertFrom-P005V122Base64 -Value $encodedToken
$coreBridgePort = Get-P005V122LoopbackPort
$env:HAOWORK_P005_CLUSTER_CORE_BRIDGE_READY_URL = "http://127.0.0.1:$coreBridgePort/ready"
$mcpGatewayPort = Get-P005V122LoopbackPort
$env:HAOWORK_P005_CLUSTER_MCP_GATEWAY_URL = "http://127.0.0.1:$mcpGatewayPort/mcp-servers/haowork-mcp"
$forward = $null
$gatewayForward = $null
try {
    $forward = Start-P005V122CoreBridgePortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $runtimeRoot -LocalPort $coreBridgePort
    Wait-P005V122CoreBridgeReady -Token $env:HAOWORK_P005_CLUSTER_CORE_BRIDGE_TOKEN -ReadyURL $env:HAOWORK_P005_CLUSTER_CORE_BRIDGE_READY_URL
    $gatewayForward = Start-P005V122MCPGatewayPortForward -Kubectl $kubectl -Kubeconfig $env:KUBECONFIG -RuntimeRoot $runtimeRoot -LocalPort $mcpGatewayPort
    Push-Location $worktreeRoot
    try {
        & $go test -tags agentteams_cluster_e2e ./internal/e2e -run 'TestP005V122' -count=1
        if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CLUSTER_E2E' }
    } finally {
        Pop-Location
    }
} finally {
    Stop-P005V122CoreBridgePortForward -Forward $gatewayForward
    Stop-P005V122CoreBridgePortForward -Forward $forward
}

Assert-P005V122EvidenceDocument -Path $clusterEvidencePath | Out-Null
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'crd', 'managers.agentteams.io', 'workers.agentteams.io', 'teams.agentteams.io', 'humans.agentteams.io', '-o', 'json') -Path (Join-Path $evidenceRoot 'crd.json') -Blocker 'BLOCKED_EVIDENCE_CRD'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'namespace', 'haowork-public', 'haowork-internal', '-o', 'json') -Path (Join-Path $evidenceRoot 'namespace.json') -Blocker 'BLOCKED_EVIDENCE_NAMESPACE'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'pods,deployments', '-n', 'haowork-public', '-o', 'json') -Path (Join-Path $evidenceRoot 'public-pod.json') -Blocker 'BLOCKED_EVIDENCE_PUBLIC_POD'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'pods,deployments', '-n', 'haowork-internal', '-o', 'json') -Path (Join-Path $evidenceRoot 'internal-pod.json') -Blocker 'BLOCKED_EVIDENCE_INTERNAL_POD'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'networkpolicy', '-n', 'haowork-public', '-o', 'json') -Path (Join-Path $evidenceRoot 'public-network-policy.json') -Blocker 'BLOCKED_EVIDENCE_PUBLIC_NETWORK_POLICY'
Invoke-P005V122KubectlEvidence -Kubectl $kubectl -Arguments @('get', 'networkpolicy', '-n', 'haowork-internal', '-o', 'json') -Path (Join-Path $evidenceRoot 'internal-network-policy.json') -Blocker 'BLOCKED_EVIDENCE_INTERNAL_NETWORK_POLICY'
Write-P005V122EvidenceManifest -EvidenceRoot $evidenceRoot -OutputPath (Join-Path $evidenceRoot 'sha256-manifest.json')
Write-Output "PASS P0-05 v1.2.2 cluster E2E evidence written to $evidenceRoot"
