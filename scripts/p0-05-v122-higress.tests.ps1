$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$upPath = Join-Path $worktreeRoot 'scripts\p0-05-v122-up.ps1'
$upText = Get-Content -LiteralPath $upPath -Raw -Encoding utf8

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Get-FunctionText {
    param([string]$Name, [string]$NextName)

    $start = $upText.IndexOf("function $Name", [StringComparison]::Ordinal)
    $end = $upText.IndexOf("function $NextName", [StringComparison]::Ordinal)
    Assert-True ($start -ge 0 -and $end -gt $start) "missing inspectable function $Name"
    return $upText.Substring($start, $end - $start)
}

$pureFunctions =
    (Get-FunctionText 'Get-P005V122ProviderHost' 'Set-P005V122HigressRouteHostHeader') +
    (Get-FunctionText 'Set-P005V122HigressRouteHostHeader' 'Wait-P005V122LocalTCPPort')
Invoke-Expression $pureFunctions

Assert-True ((Get-P005V122ProviderHost -BaseURL 'https://provider.example/v1') -ceq 'provider.example') 'provider host parsing failed'
foreach ($invalid in @('http://', '/v1', 'https://user:pass@provider.example/v1')) {
    $failed = $false
    try { $null = Get-P005V122ProviderHost -BaseURL $invalid } catch { $failed = $_.Exception.Message -match 'BLOCKED_RUNTIME_PROVIDER_URL' }
    Assert-True $failed "unsafe provider URL was accepted: $invalid"
}

$route = [pscustomobject]@{
    name = 'default-ai-route'
    upstreams = @([pscustomobject]@{ provider = 'openai-compat'; weight = 100 })
    authConfig = [pscustomobject]@{ enabled = $true; allowedConsumers = @('manager') }
}
$updated = Set-P005V122HigressRouteHostHeader -Route $route -HostName 'provider.example'
Assert-True ($updated.name -ceq 'default-ai-route') 'route identity changed during host convergence'
Assert-True ($updated.upstreams[0].provider -ceq 'openai-compat') 'route upstream changed during host convergence'
Assert-True ($updated.authConfig.allowedConsumers[0] -ceq 'manager') 'route authorization changed during host convergence'
Assert-True ([bool]$updated.headerControl.enabled) 'route header control was not enabled'
Assert-True ($updated.headerControl.request.set.Count -eq 1) 'route host convergence must produce one deterministic set operation'
Assert-True ($updated.headerControl.request.set[0].key -ceq 'Host') 'route host convergence did not target Host'
Assert-True ($updated.headerControl.request.set[0].value -ceq 'provider.example') 'route host convergence used the wrong provider host'

Assert-True ($upText -match 'Set-P005V122HigressProviderHostHeader\s+-Kubectl\s+\$kubectl\s+-Zone\s+\$zone') 'real deployment must converge Higress in every zone'
Assert-True ($upText -match 'BLOCKED_HIGRESS_ROUTE_') 'Higress convergence must expose a fail-closed blocked state'

$publisherPaths = @(
    (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-higress-binding-publisher.yaml'),
    (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-higress-binding-publisher-internal.yaml')
)
foreach ($publisherPath in $publisherPaths) {
    $publisher = Get-Content -LiteralPath $publisherPath -Raw -Encoding utf8
    $expectedRelease = if ($publisherPath -match '-internal\.yaml$') { 'haowork-internal-agentteams' } else { 'haowork-public-agentteams' }
    $initStart = $publisher.IndexOf("      initContainers:", [StringComparison]::Ordinal)
    $containersStart = if ($initStart -ge 0) { $publisher.IndexOf("      containers:", $initStart, [StringComparison]::Ordinal) } else { -1 }
    Assert-True ($initStart -ge 0 -and $containersStart -gt $initStart) "$publisherPath must prepare the publisher state before startup"
    $initContainer = $publisher.Substring($initStart, $containersStart - $initStart)
    Assert-True ($initContainer -match 'name: prepare-publisher-state') "$publisherPath must use the dedicated state preparation command"
    Assert-True ($initContainer -match 'image: haowork-higress-binding-publisher:local') "$publisherPath must reuse the local publisher image"
    Assert-True ($initContainer -match 'command: \["/haowork-higress-binding-publisher"\]') "$publisherPath must execute the publisher binary"
    Assert-True ($initContainer -match 'args: \["prepare-state-dir", "/var/lib/haowork/publisher-state/managed-consumers\.json"\]') "$publisherPath must prepare only the fixed state path"
    Assert-True ($initContainer -match 'allowPrivilegeEscalation: false') "$publisherPath state preparation must disable privilege escalation"
    Assert-True ($initContainer -match 'readOnlyRootFilesystem: true') "$publisherPath state preparation must keep the root filesystem read-only"
    Assert-True ($initContainer -match 'runAsUser: 0') "$publisherPath state preparation must run as root"
    Assert-True ($initContainer -match 'drop: \["ALL"\]') "$publisherPath state preparation must drop ambient capabilities"
    Assert-True ($initContainer -match 'add: \["CHOWN"\]') "$publisherPath state preparation must add only CHOWN"
    Assert-True ($initContainer -notmatch 'FOWNER|DAC_OVERRIDE|runtime-bindings|secretKeyRef') "$publisherPath state preparation has excess capability or mounts"
    Assert-True ($initContainer -match '(?ms)name: publisher-state.*?mountPath: /var/lib/haowork/publisher-state.*?readOnly: false') "$publisherPath state preparation must mount only publisher state writable"
    Assert-True ($publisher -match '(?m)^kind: ServiceAccount$') "$publisherPath must declare an independent ServiceAccount"
    Assert-True ($publisher -notmatch '(?m)^kind: (Cluster)?Role(Binding)?$') "$publisherPath must not grant Kubernetes RBAC"
    Assert-True (($publisher -split 'automountServiceAccountToken: false').Count -ge 3) "$publisherPath must disable pod and ServiceAccount token mounts"
    Assert-True ($publisher -match 'name: higress-console') "$publisherPath must mount only the independent Higress Console credential"
    Assert-True ($publisher -match 'claimName: haowork-runtime-bindings') "$publisherPath must mount the sanitized runtime binding PVC"
    Assert-True ($publisher -match '(?ms)name: runtime-bindings.*?readOnly: true') "$publisherPath must mount runtime bindings read-only"
    Assert-True ($publisher -match 'claimName: haowork-higress-binding-state') "$publisherPath must mount its independent managed-consumer state PVC"
    Assert-True ($publisher -match '(?ms)name: publisher-state.*?readOnly: false') "$publisherPath must persist only publisher ownership state"
    $mainContainer = $publisher.Substring($containersStart)
    Assert-True ($mainContainer -match '(?ms)name: route-binding-publisher.*?runAsNonRoot: true.*?runAsUser: 65532.*?runAsGroup: 65532') "$publisherPath must keep the publisher process non-root"
    foreach ($forbidden in @('agentteams-creds-', 'WORKER_GATEWAY_KEY', 'matrix-', 'MINIO_', 'HAOWORK_CORE_', 'model', 'governance-state')) {
        Assert-True ($publisher -notmatch [regex]::Escape($forbidden)) "$publisherPath contains forbidden credential or state reference: $forbidden"
    }
    Assert-True ($publisher -match '(?ms)kind: NetworkPolicy.*?app.kubernetes.io/name: haowork-higress-binding-publisher') "$publisherPath must select only the route publisher"
    Assert-True ($publisher -match ('(?ms)egress:.*?app.kubernetes.io/name: higress-console.*?app.kubernetes.io/instance: {0}.*?port: 8080' -f [regex]::Escape($expectedRelease))) "$publisherPath must allow only its release Higress Console TCP 8080"
    Assert-True ($publisher -match '(?ms)kubernetes.io/metadata.name: kube-system.*?k8s-app: kube-dns.*?port: 53.*?port: 53') "$publisherPath must allow only CoreDNS UDP and TCP 53"
}

foreach ($bindingPath in @(
    (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-binding-controller.yaml'),
    (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-binding-controller-internal.yaml')
)) {
    $bindingController = Get-Content -LiteralPath $bindingPath -Raw -Encoding utf8
    Assert-True ($bindingController -notmatch 'higress-console') "$bindingPath must not mount Higress Console credentials"
}

foreach ($zonePolicyPath in @(
    (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\public-network-policy.yaml'),
    (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\internal-network-policy.yaml')
)) {
    $zonePolicy = Get-Content -LiteralPath $zonePolicyPath -Raw -Encoding utf8
    Assert-True ($zonePolicy -match '(?ms)podSelector:.*?operator: NotIn.*?haowork-higress-binding-publisher') "$zonePolicyPath must exclude the publisher from broad same-zone egress"
}

$upBroadPolicies = @(
    (Get-FunctionText 'Write-P005V122KubernetesAPIPolicy' 'Write-P005V122ExternalEgressPolicy'),
    (Get-FunctionText 'Write-P005V122ExternalEgressPolicy' 'Get-P005V122KindClusters')
)
foreach ($policyFunction in $upBroadPolicies) {
    Assert-True ($policyFunction -match '(?ms)podSelector:.*?operator: NotIn.*?haowork-higress-binding-publisher') 'generated broad policy must exclude the Higress binding publisher'
    Assert-True ($policyFunction -notmatch '(?m)podSelector:\s*\{\}') 'generated broad policy must not select every pod'
}
Assert-True ($upText -notmatch '(?m)podSelector:\s*\{\}') 'all generated deployment policies must avoid selecting the publisher implicitly'
Assert-True (($upText -split 'haowork-higress-binding-publisher').Count -ge 3) 'all generated broad policies must carry the publisher exclusion'

'P0-05 AgentTeams v1.2.2 Higress convergence checks passed.'
