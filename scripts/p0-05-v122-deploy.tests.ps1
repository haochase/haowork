[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$script:WorktreeRoot = Split-Path -Parent $PSScriptRoot
$script:CommonGitDir = (& git -C $script:WorktreeRoot rev-parse --git-common-dir).Trim()
$script:RepoRoot = Split-Path -Parent ([IO.Path]::GetFullPath($script:CommonGitDir))
$script:CacheRoot = Join-Path $script:RepoRoot '.haowork\cache'
$script:DeploymentRoot = Join-Path $script:WorktreeRoot 'deploy\agentteams\v1.2.2'
$script:LockPath = Join-Path $script:DeploymentRoot 'upstream.lock.json'
$script:PublicValuesPath = Join-Path $script:DeploymentRoot 'public-values.yaml'
$script:InternalValuesPath = Join-Path $script:DeploymentRoot 'internal-values.yaml'
$script:PublicPolicyPath = Join-Path $script:DeploymentRoot 'public-network-policy.yaml'
$script:InternalPolicyPath = Join-Path $script:DeploymentRoot 'internal-network-policy.yaml'

foreach ($name in @('tmp', 'go', 'gomod', 'helm', 'helm\config', 'helm\data')) {
    New-Item -ItemType Directory -Force (Join-Path $script:CacheRoot $name) | Out-Null
}
$env:TEMP = Join-Path $script:CacheRoot 'tmp'
$env:TMP = $env:TEMP
$env:GOCACHE = Join-Path $script:CacheRoot 'go'
$env:GOMODCACHE = Join-Path $script:CacheRoot 'gomod'
$env:HELM_CACHE_HOME = Join-Path $script:CacheRoot 'helm'
$env:HELM_CONFIG_HOME = Join-Path $script:CacheRoot 'helm\config'
$env:HELM_DATA_HOME = Join-Path $script:CacheRoot 'helm\data'

function Assert-True {
    param(
        [Parameter(Mandatory)][bool]$Condition,
        [Parameter(Mandatory)][string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function Remove-P005V122TestPaths {
    param([Parameter(Mandatory)][string[]]$Paths)

    foreach ($path in $Paths) {
        if (-not (Test-Path -LiteralPath $path)) { continue }
        $fullPath = [IO.Path]::GetFullPath($path)
        $tmpRoot = [IO.Path]::GetFullPath((Join-Path $script:CacheRoot 'tmp'))
        Assert-True ($fullPath.StartsWith($tmpRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) "refusing to remove a test artifact outside the project cache tmp directory: $fullPath"
        Remove-Item -LiteralPath $fullPath -Force -ErrorAction SilentlyContinue
    }
}

function Get-FileText {
    param([Parameter(Mandatory)][string]$Path)

    Assert-True (Test-Path -LiteralPath $Path) "required file is missing: $Path"
    return Get-Content -LiteralPath $Path -Raw -Encoding utf8
}

function Assert-NoLiteralSecret {
    param(
        [Parameter(Mandatory)][string]$Text,
        [Parameter(Mandatory)][string]$Path
    )

    $forbidden = @(
        '(?im)^\s*(llmApiKey|adminPassword|registrationToken|rootPassword)\s*:\s*(?!["'']{0,1}(?:"|''|\$\{|__)[^\r\n]*$)[^"''\s][^\r\n]*$',
        '(?i)(sk-[a-z0-9_-]{12,}|api[_-]?key\s*[:=]\s*[a-z0-9_-]{12,})'
    )
    foreach ($pattern in $forbidden) {
        Assert-True (-not [regex]::IsMatch($Text, $pattern)) "literal credential-like value found in $Path"
    }
}

function Assert-OfficialValues {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Namespace,
        [Parameter(Mandatory)][string]$Release,
        [Parameter(Mandatory)][string]$Bucket,
        [Parameter(Mandatory)][string]$BrowserURL
    )

    $text = Get-FileText $Path
    foreach ($key in @('credentials:', 'matrix:', 'gateway:', 'storage:', 'controller:', 'manager:', 'worker:')) {
        Assert-True ($text -match "(?m)^$([regex]::Escape($key))") "$Path must use the official $key values root"
    }
    Assert-True ($text -match '(?ms)^controller:.*?^\s+image:') "$Path must keep the controller image selection explicit"
    Assert-True ($text -match '(?ms)^manager:.*?^\s+image:') "$Path must keep the manager image selection explicit"
    Assert-True ($text -match '(?ms)^worker:.*?^\s+defaultImage:') "$Path must keep the Worker image selection explicit"
    Assert-True ($text -match '(?ms)^global:.*?^\s+imageTag:\s*["'']?v1\.2\.2["'']?\s*$') "$Path must select the pinned v1.2.2 release images independently of Chart appVersion"
    Assert-True ($text -match '(?ms)^worker:.*?^\s+defaultRuntime:\s*["'']?openclaw["'']?\s*$') "$Path must keep OpenClaw as the active Worker runtime"
    Assert-True (([regex]::Matches($text, '@sha256:[a-f0-9]{64}')).Count -ge 12) "$Path must digest-pin every active image and every published optional Worker image"
    Assert-True ($text -match '(?ms)^\s+openhuman:\s*\r?\n\s+repository:\s*["'']?higress-registry[^\r\n]+\r?\n\s+tag:\s*["'']?v1\.2\.2["'']?\s*$') "$Path must keep unavailable OpenHuman optional and unpinned rather than activating it"
    foreach ($forbiddenKey in @('agentTeams:', 'networkPolicy:', 'hiclaw.io', 'hi-claw')) {
        Assert-True ($text -notmatch [regex]::Escape($forbiddenKey)) "$Path contains obsolete deployment key or legacy control-plane reference: $forbiddenKey"
    }
    $namespacePattern = '(?m)^\s*namespace:\s*["'']?{0}["'']?\s*$' -f [regex]::Escape($Namespace)
    $matrixPattern = '(?m)^\s*serverName:\s*["'']?{0}["'']?\s*$' -f [regex]::Escape("$Namespace.matrix.local")
    $bucketPattern = '(?m)^\s*bucket:\s*["'']?{0}["'']?\s*$' -f [regex]::Escape($Bucket)
    $releasePattern = '(?m)^\s*fullnameOverride:\s*["'']?{0}["'']?\s*$' -f [regex]::Escape($Release)
    Assert-True ($text -match $namespacePattern) "$Path must select $Namespace"
    Assert-True ($text -match $matrixPattern) "$Path must use a zone-specific Matrix server name"
    Assert-True ($text -match $bucketPattern) "$Path must use bucket $Bucket"
    Assert-True ($text -match $releasePattern) "$Path must use release-specific controller naming"
    $browserURLPattern = '(?m)^\s*publicURL:\s*["'']?{0}["'']?\s*$' -f [regex]::Escape($BrowserURL)
    Assert-True ($text -match $browserURLPattern) "$Path must set the browser-facing gateway URL to $BrowserURL"
    Assert-True ($text -notmatch '(?im)^\s*publicURL:\s*[^\r\n]*\.svc\.cluster\.local') "$Path must not put an in-cluster DNS name into gateway.publicURL"
    $zone = if ($Namespace -eq 'haowork-public') { 'public' } else { 'internal' }
    Assert-True ($text -match ('ingressClass:\s*["'']?haowork-{0}-higress' -f $zone)) "$Path must use a zone-specific Higress IngressClass"
    Assert-True ($text -match ('watchNamespace:\s*["'']?{0}' -f [regex]::Escape($Namespace))) "$Path must limit Higress watching to its own namespace"
    Assert-True ($text -match '(?m)^\s*oneNamespace:\s*true\s*$') "$Path must constrain Higress to one namespace"
    Assert-True ($text -match '(?ms)^higress:\s*\r?\n\s+global:.*?^\s+local:\s*false\s*$') "$Path must avoid Higress host ports so both zones can share one Kind node"
    Assert-True ($text -match '(?ms)^\s{4}gateway:.*?^\s{6}resources:.*?^\s{10}memory:\s*256Mi\s*$') "$Path must bound local Higress gateway requests"
    Assert-True ($text -match '(?ms)^\s{4}controller:.*?^\s{6}resources:.*?^\s{10}memory:\s*256Mi\s*$') "$Path must bound local Higress controller requests"
    Assert-True ($text -match '(?ms)^\s{4}pilot:.*?^\s{6}resources:.*?^\s{10}memory:\s*256Mi\s*$') "$Path must bound local Higress discovery requests"
    Assert-True ($text -notmatch '(?m)^\s*existingSecret\s*:') "$Path must not invent an unsupported existingSecret values key"
    Assert-True ($text -match '(?m)^\s*enabled:\s*false\s*(?:#.*)?$') "$Path must disable the chart's automatic preflight hook until runtime credentials are injected"
    Assert-NoLiteralSecret $text $Path
}

function Assert-NetworkPolicy {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Namespace,
        [Parameter(Mandatory)][string]$Zone
    )

    $text = Get-FileText $Path
    Assert-True ($text -match '(?m)^kind:\s*NetworkPolicy\s*$') "$Path must be a NetworkPolicy"
    Assert-True ($text -match "(?m)^\s*namespace:\s*$([regex]::Escape($Namespace))\s*$") "$Path must target namespace $Namespace"
    Assert-True ($text -match '(?m)^\s*-\s*Ingress\s*$') "$Path must enforce ingress policy"
    Assert-True ($text -match '(?m)^\s*-\s*Egress\s*$') "$Path must enforce egress policy"
    Assert-True ($text -match "haowork\.io/zone:\s*$([regex]::Escape($Zone))") "$Path must only allow same-zone namespace traffic"
    Assert-True ($text -match 'kubernetes\.io/metadata\.name:\s*kube-system') "$Path must allow kube-system DNS only by explicit selector"
    Assert-True ($text -match '(?m)^\s*port:\s*53\s*$') "$Path must permit DNS port 53"
    Assert-True ($text -notmatch 'ipBlock:') "$Path must keep external egress absent until explicit runtime CIDRs are supplied"
    Assert-True ($text -notmatch '0\.0\.0\.0/0|::/0') "$Path must never allow all-network CIDRs"
    Assert-True ($text -notmatch "haowork\.io/zone:\s*(?!$([regex]::Escape($Zone))\b)\S+") "$Path must not permit cross-zone namespace traffic"
}

function Assert-LockContract {
    Assert-True (Test-Path -LiteralPath $script:LockPath) "Task 1 lock file is missing: $script:LockPath"
    $lock = Get-Content -LiteralPath $script:LockPath -Raw -Encoding utf8 | ConvertFrom-Json
    Assert-True ($lock.tag -eq 'v1.2.2') 'lock must pin AgentTeams tag v1.2.2'
    Assert-True ($lock.chart_version -eq '1.1.1') 'lock must document the upstream Chart version 1.1.1'
    Assert-True ($lock.chart_app_version -eq '1.1.1') 'lock must document the upstream Chart appVersion 1.1.1'
    Assert-True ($lock.chart_path -eq 'helm/agentteams') 'lock must point to the official Helm chart path'
}

function Assert-DeploymentScripts {
    foreach ($name in @('p0-05-v122-preflight.ps1', 'p0-05-v122-up.ps1', 'p0-05-v122-down.ps1')) {
        $path = Join-Path $script:WorktreeRoot "scripts\$name"
        $text = Get-FileText $path
        Assert-True ($text -match '\.haowork\\cache') "$name must write runtime state under .haowork\\cache"
        Assert-True ($text -match 'BLOCKED_') "$name must expose explicit blocked states"
    }
    $up = Get-FileText (Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-up.ps1')
    Assert-True ($up -match 'RenderOnly') 'up script must support RenderOnly mode'
    Assert-True ($up -match 'LocalDualNamespace') 'up script must support LocalDualNamespace mode'
    Assert-True ($up -match '\.tools\\bin\\\$Name\.exe') 'up script must prefer project-local executable resolution'
    Assert-True ($up -match 'upgrade\s+--install\s+--atomic\s+--wait') 'up script must deploy with atomic wait semantics'
    Assert-True ($up -match 'helm\s+template') 'RenderOnly mode must use helm template'
    Assert-True ($up -notmatch 'HAOWORK_P005_IMAGE_DIGEST') 'v1.2.2 deployment must not rely on a single legacy image digest variable'
    Assert-True ($up -match 'Copy-P005V122ChartToCache') 'up script must copy the official chart into the project cache before building dependencies'
    Assert-True ($up -match 'ConvertTo-P005V122SafeManifest') 'up script must redact sensitive rendered values before writing evidence'
    Assert-True ($up -match 'System\.Threading\.Mutex') 'up must serialize shared Chart cache rebuilds across PowerShell processes'
    Assert-True ($up -match 'BLOCKED_CHART_CACHE_LOCK_TIMEOUT') 'up must fail clearly if the shared Chart cache lock cannot be acquired'
    Assert-True ($up -match 'Invoke-P005V122WithChartCacheLock') 'up must hold the Chart cache lock across dependency build and Helm rendering'
    $upLockStart = $up.IndexOf('Invoke-P005V122WithChartCacheLock -Action {', [System.StringComparison]::Ordinal)
    $upPreflightStart = $up.IndexOf('& $preflightPath -Mode $Mode', [System.StringComparison]::Ordinal)
    Assert-True ($upLockStart -ge 0 -and $upPreflightStart -gt $upLockStart) 'up must run its shared-evidence preflight under the Chart cache lock'
    Assert-True ($up -match '\$env:GOCACHE\s*=\s*Join-Path \$cacheRoot ''go''') 'up must redirect Go cache to the project cache on E drive'
    Assert-True ($up -match '\$env:GOMODCACHE\s*=\s*Join-Path \$cacheRoot ''gomod''') 'up must redirect Go module cache to the project cache on E drive'
    Assert-True ($up -match 'kind export kubeconfig') 'up must export a reused Kind cluster kubeconfig into the project cache'
    Assert-True ($up -match 'BLOCKED_KIND_QUERY') 'up must fail clearly if Kind cluster discovery fails'
    Assert-True ($up -match 'repo add higress') 'up script must resolve the official Higress dependency from the chart lock'
    Assert-True ($up -match 'Get-P005V122RenderOnlyValues') 'RenderOnly must supply a non-runtime template-only value for the upstream required LLM field'
    Assert-True ($up -notmatch 'create secret generic') 'the upstream Chart must own each release runtime Secret; up must not pre-create a conflicting Secret'
    Assert-True ($up -notmatch 'Join-Path\s+\$evidenceRoot.*values\.install\.yaml') 'real runtime values containing credentials must never be written into the evidence directory'
    Assert-True ($up -match 'Write-P005V122KubernetesAPIPolicy') 'up must add a scoped Kubernetes API egress policy before deploying the chart'
    Assert-True ($up -match 'Write-P005V122ExternalEgressPolicy') 'up must allow only explicitly configured external dependency CIDRs'
    Assert-True ($up -match 'Test-P005V122CIDR') 'up must reject non-canonical, multicast, and all-network external CIDRs'
    Assert-True ($up -match 'Start-P005V122BrowserPortForward') 'up must start controlled browser port-forwards after a local deployment'
    Assert-True ($up -match 'Stop-P005V122BrowserPortForward') 'up must replace only managed browser port-forwards'
    Assert-True ($up -match 'Test-P005V122ManagedBrowserEndpoint') 'up must verify browser traffic belongs to its managed port-forward'
    $managerCreateWait = $up.IndexOf('& $kubectl wait --for=create "manager/default"', [StringComparison]::Ordinal)
    $managerRunningWait = $up.IndexOf('& $kubectl wait --for="jsonpath={.status.phase}=Running" "manager/default"', [StringComparison]::Ordinal)
    Assert-True ($managerCreateWait -ge 0 -and $managerRunningWait -gt $managerCreateWait) 'up must wait for Manager creation before waiting for its Running phase'
    Assert-True ($up -match 'HttpWebRequest') 'up must perform an HTTP-level browser endpoint check'
    Assert-True ($up -match 'process_start_time_utc') 'up must persist the managed port-forward process start time'
    Assert-True ($up -match '--kubeconfig') 'up must bind a managed port-forward to the project Kind kubeconfig'
    Assert-True ($up -match 'Test-P005V122ManagedBrowserCacheDirectory') 'up must only remove an exact token-bound managed port-forward cache directory'
    Assert-True ($up -match 'RequireBrowserEndpoint') 'up must verify browser endpoint availability after starting port-forwards'
    Assert-True ($up -match 'BLOCKED_NAMESPACE_CREATE') 'up must fail closed when namespace creation cannot be rendered'
    Assert-True ($up -match 'BLOCKED_NAMESPACE_LABEL') 'up must fail closed when zone labeling fails'
    Assert-True ($up -match 'BLOCKED_EVIDENCE_PODS') 'up must fail closed when Pod evidence cannot be collected'
    Assert-True ($up -match 'BLOCKED_EVIDENCE_MANAGER') 'up must fail closed when Manager evidence cannot be collected'
    Assert-True ($up -match 'managers\.agentteams\.io') 'up must wait for the official Manager CRD'
    Assert-True ($up -match 'status\.phase.*Running') 'up must wait for the official Manager Running phase'
    $down = Get-FileText (Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-down.ps1')
    Assert-True ($down -match 'HELM_CACHE_HOME') 'down must keep Helm state off C drive'
    Assert-True ($down -notmatch 'DockerDesktopWSL|Remove-Item.*Docker') 'down must preserve Docker Desktop data'
    Assert-True ($down -match 'get clusters') 'down must check for the named Kind cluster before reporting cleanup success'
    Assert-True ($down -match 'kind export kubeconfig') 'down must export the named Kind kubeconfig into the project cache before cleanup'
    Assert-True ($down -match 'BLOCKED_HELM_UNINSTALL') 'down must fail if a release cannot be uninstalled'
    Assert-True ($down -match 'BLOCKED_NAMESPACE_DELETE') 'down must fail if a namespace cannot be deleted'
    Assert-True ($down -match 'BLOCKED_KIND_DELETE') 'down must fail if a requested Kind cluster cannot be deleted'
    $directClusterDelete = $down.IndexOf('if ($DeleteCluster)', [StringComparison]::Ordinal)
    $helmUninstall = $down.IndexOf('& $helm uninstall', [StringComparison]::Ordinal)
    Assert-True ($directClusterDelete -ge 0 -and $helmUninstall -gt $directClusterDelete) 'owned DeleteCluster cleanup must bypass namespace-level Helm finalizers'
    Assert-True ($down -match 'Stop-P005V122BrowserPortForward') 'down must stop only controlled browser port-forwards'
    Assert-True ($down -match 'process_start_time_utc') 'down must protect against port-forward PID reuse'
    Assert-True ($up -match 'Assert-P005V122ClusterName') 'up must validate the cluster name before deriving cache paths'
    Assert-True ($down -match 'Assert-P005V122ClusterName') 'down must validate the cluster name before deriving cache paths'
    $preflight = Get-FileText (Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1')
    Assert-True ($preflight -match 'Test-P005V122DockerDaemon') 'preflight must verify the Docker daemon, not only a docker executable path'
    Assert-True ($preflight -match 'BLOCKED_DOCKER_DAEMON') 'preflight must expose an unavailable Docker daemon as blocked'
    Assert-True ($preflight -match 'Get-P005V122DockerCgroupVersion') 'preflight must inspect the Docker daemon cgroup version before creating a Kind cluster'
    Assert-True ($preflight -match 'BLOCKED_CGROUP_V1') 'preflight must reject Docker daemons that still expose cgroup v1'
    Assert-True ($preflight -match 'CustomWslDistroDir') 'preflight must read Docker Desktop CustomWslDistroDir rather than trust a directory probe'
    Assert-True ($preflight -match 'ext4\.vhdx') 'preflight must require the configured Docker Desktop WSL VHDX to exist'
    Assert-True ($preflight -match 'BLOCKED_DOCKER_STORAGE') 'preflight must block when Docker Desktop storage cannot be verified off C drive'
    Assert-True ($preflight -match 'Test-P005V122CIDR') 'preflight must validate explicit egress CIDRs before local deployment'
    Assert-True ($preflight -match 'BLOCKED_EGRESS_CIDRS') 'missing or unsafe external dependency CIDRs must block a real deployment'
    Assert-True ($preflight -match 'Test-P005V122BrowserEndpoint') 'preflight must validate controlled loopback browser endpoints'
    Assert-True ($preflight -match 'Test-P005V122ManagedBrowserEndpoint') 'preflight must reject an unrelated listener on a browser port'
    Assert-True ($preflight -match 'Test-P005V122ManagedBrowserCacheDirectory') 'preflight must require the exact token-bound project cache directory for a managed browser port-forward'
    Assert-True ($preflight -match 'HttpWebRequest') 'preflight must perform an HTTP-level browser endpoint check'
    Assert-True ($preflight -match 'RequireBrowserEndpoint') 'preflight must support post-install browser endpoint verification'
    Assert-True ($preflight -notmatch 'if \(\$SkipDeploymentLock\)') 'preflight must not expose a lock-bypass execution path'
    Assert-True ($preflight -match 'BLOCKED_BROWSER_ENDPOINT') 'preflight must block local browser acceptance when the port-forward is absent'
    Assert-True ($preflight -match 'Test-P005V122RuntimeProviderEndpoint') 'preflight must probe an explicit runtime provider endpoint before real deployment'
    Assert-True ($preflight -match 'BLOCKED_RUNTIME_PROVIDER_ENDPOINT') 'unreachable or unapproved runtime provider endpoints must block real deployment'
    Assert-True ($preflight -match "Headers\['Authorization'\]") 'runtime provider probe must verify the configured API key is accepted'
    Assert-True ($preflight -match '/models') 'runtime provider probe must use an OpenAI-compatible health route'
    Assert-True ($preflight -match 'request\.Proxy\s*=\s*\$null') 'runtime provider probe must not route through an ambient system proxy'
    foreach ($scriptUnderTest in @($up, $down, $preflight)) {
        Assert-True ($scriptUnderTest -match 'Invoke-P005V122WithDeploymentLock') 'all deployment lifecycle scripts must serialize shared cache and evidence state'
    }

    Assert-True ($preflight -match 'Invoke-P005V122WithEvidenceLock') 'preflight must serialize the shared latest evidence across cluster names'
    Assert-True ($preflight -match 'Move-Item.*preflight') 'preflight must publish evidence atomically after its complete result is assembled'
    Assert-True ($down -match 'Test-P005V122ManagedBrowserPortForward') 'down must verify managed port-forward process identity before stopping it'
    Assert-True ($down -match 'kubeconfig_path') 'down must require the recorded project kubeconfig before stopping a port-forward'
    Assert-True ($down -match 'run_token') 'down must require the recorded per-run token before stopping a port-forward'
    Assert-True ($down -match 'Test-P005V122ManagedBrowserCacheDirectory') 'down must only remove an exact token-bound managed port-forward cache directory'
    foreach ($lifecycleScript in @($up, $down)) {
        Assert-True ($lifecycleScript -match 'function Remove-P005V122ManagedBrowserCacheDirectory') 'browser cache cleanup must use an idempotent managed-directory helper'
        Assert-True ($lifecycleScript -match 'DirectoryNotFoundException') 'browser cache cleanup must tolerate a concurrent directory removal'
        Assert-True ($lifecycleScript -match 'BLOCKED_BROWSER_CACHE_CLEANUP') 'browser cache cleanup must still fail closed for persistent removal errors'
    }
    Assert-True ($down -match 'Get-P005V122DeploymentOwnership') 'down must load durable deployment ownership before deleting resources'
    Assert-True ($down -match 'BLOCKED_DEPLOYMENT_OWNERSHIP') 'down must refuse cleanup without a durable ownership record'
    Assert-True ($down -match 'BLOCKED_NAMESPACE_OWNERSHIP') 'down must refuse cleanup for namespaces that are not owned by this deployment'
    Assert-True ($down -match '--wait') 'down must wait for Helm resource deletion rather than report acceptance as completion'
    Assert-True ($down -match 'kubectl wait') 'down must observe namespace deletion before reporting cleanup success'
    $downOwnershipStart = $down.IndexOf('Get-P005V122DeploymentOwnership', [System.StringComparison]::Ordinal)
    $downPortForwardStop = $down.LastIndexOf('Stop-P005V122BrowserPortForward', [System.StringComparison]::Ordinal)
    Assert-True ($downOwnershipStart -ge 0 -and $downPortForwardStop -gt $downOwnershipStart) 'down must prove deployment ownership before stopping managed port-forwards'
    Assert-True ($up -match 'Get-P005V122DeploymentOwnership') 'up must verify durable deployment ownership before reusing a Kind cluster'
    Assert-True ($up -match 'Write-P005V122DeploymentOwnership') 'up must persist ownership after creating a controlled cluster or namespace'
    Assert-True ($up -match 'haowork\.io/p005-owner') 'up must label controlled namespaces with a deployment ownership token'
    Assert-True ($up -match 'BLOCKED_CLUSTER_OWNERSHIP') 'up must block reuse of an unowned Kind cluster'
    Assert-True ($up -match 'BLOCKED_NAMESPACE_OWNERSHIP') 'up must block reuse of an unowned namespace'
    Assert-True ($up -match 'cluster_created_by_haowork\s+-is\s+\[bool\]') 'up must require a real Boolean cluster ownership marker'
    Assert-True ($down -match 'cluster_created_by_haowork\s+-is\s+\[bool\]') 'down must require a real Boolean cluster ownership marker'
    Assert-True ($up -match 'Invoke-P005V122Rollback') 'up must compensate resources created during a failed dual-zone deployment'
    Assert-True ($up -notmatch 'Select-Object\s+-Reverse') 'rollback must not call the nonexistent PowerShell Select-Object -Reverse parameter'
    Assert-True ($up -match '\[array\]::Reverse') 'rollback must reverse transaction collections with a PowerShell 5.1-compatible API'
    Assert-True ($up -match 'BLOCKED_PARTIAL_DEPLOYMENT') 'up must expose partial deployment rollback as a blocked result'
    Assert-True ($up -match 'helm rollback') 'up must restore a pre-existing owned release after a later zone fails'
    Assert-True ($up -match 'ownership_verified') 'up rollback must distinguish a verified controlled cluster from an unowned cluster reuse'
    Assert-True ($up -match 'ConvertTo-Json\s+-Compress') 'runtime values must serialize arbitrary credential scalars safely for YAML'
}

function Assert-OfficialSourceGate {
    $preflight = Get-FileText (Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1')
    $up = Get-FileText (Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-up.ps1')
    $downPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-down.ps1'
    $down = Get-FileText $downPath
    foreach ($scriptUnderTest in @($preflight, $up, $down)) {
        Assert-True ($scriptUnderTest -match 'Test-P005V122OfficialSource') 'production deployment path must validate the cached official source'
        Assert-True ($scriptUnderTest -match 'BLOCKED_UPSTREAM_SOURCE') 'production deployment path must expose source drift as BLOCKED_UPSTREAM_SOURCE'
    }
    $downSourceGateIndex = $down.LastIndexOf('Assert-P005V122LockedOfficialSource', [System.StringComparison]::Ordinal)
    $downDestructiveKindIndex = $down.LastIndexOf('Get-P005V122KindClusters -Kind $kind', [System.StringComparison]::Ordinal)
    Assert-True ($downSourceGateIndex -ge 0 -and $downDestructiveKindIndex -gt $downSourceGateIndex) 'down must validate the official source before invoking Kind cleanup commands'

    . (Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-common.ps1')
    $common = Get-FileText (Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-common.ps1')
    Assert-True ($common -match 'rev-parse\s+--git-common-dir') 'official source validation must resolve the shared repository cache from the Git common directory'
    $contract = Get-P005V122OfficialContract -ContractPath $script:LockPath
    $upstreamRoot = Join-Path $script:CacheRoot 'upstream\AgentTeams-v1.2.2'
    Assert-True (Test-P005V122OfficialSource -Contract $contract -UpstreamRoot $upstreamRoot) 'the baseline official source must satisfy the lock before drift checks'

    $wrongCommit = $contract.Clone()
    $wrongCommit['commit'] = '0000000000000000000000000000000000000000'
    Assert-True (-not (Test-P005V122OfficialSource -Contract $wrongCommit -UpstreamRoot $upstreamRoot)) 'wrong upstream commit was accepted'

    $dirtyRoot = Join-Path $script:CacheRoot ("tmp\p0-05-v122-deploy-dirty-test-" + [guid]::NewGuid().ToString('N'))
    try {
        New-Item -ItemType Directory -Force $dirtyRoot | Out-Null
        & git -C $dirtyRoot init --quiet
        & git -C $dirtyRoot config user.name 'P005 Deploy Test'
        & git -C $dirtyRoot config user.email 'p005-deploy-test@localhost'
        'clean deployment-source fixture' | Set-Content -LiteralPath (Join-Path $dirtyRoot 'fixture.txt') -Encoding utf8
        & git -C $dirtyRoot add fixture.txt
        & git -C $dirtyRoot commit --quiet -m 'deployment source fixture'
        Assert-True ($LASTEXITCODE -eq 0) 'could not create an isolated dirty-source test fixture'
        Assert-True (Test-P005V122GitClean -RepositoryRoot $dirtyRoot) 'clean source fixture was rejected'
        'uncommitted deployment-source fixture' | Set-Content -LiteralPath (Join-Path $dirtyRoot 'p0-05-v122-dirty-fixture.txt') -Encoding utf8
        Assert-True (-not (Test-P005V122GitClean -RepositoryRoot $dirtyRoot)) 'dirty upstream source was accepted'
        $sourceFunction = (Get-Command Test-P005V122OfficialSource).ScriptBlock.ToString()
        Assert-True ($sourceFunction -match 'Test-P005V122GitClean') 'official source validation must reject a dirty upstream worktree'
    } finally {
        if (Test-Path -LiteralPath $dirtyRoot) {
            $resolvedDirtyRoot = [IO.Path]::GetFullPath($dirtyRoot)
            $allowedRoot = [IO.Path]::GetFullPath((Join-Path $script:CacheRoot 'tmp'))
            Assert-True ($resolvedDirtyRoot.StartsWith($allowedRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) 'refusing to remove a dirty-source test path outside the project cache'
            Remove-Item -LiteralPath $resolvedDirtyRoot -Recurse -Force
        }
    }

    $dirtyMarker = Join-Path $upstreamRoot ('.p0-05-v122-down-source-gate-' + [guid]::NewGuid().ToString('N'))
    try {
        'temporary dirty source gate fixture' | Set-Content -LiteralPath $dirtyMarker -Encoding utf8
        $dirtyDown = Invoke-P005V122Script -Path $downPath
        Assert-True ($dirtyDown.ExitCode -ne 0) 'down must fail closed when the official source cache is dirty'
        Assert-True ($dirtyDown.Output -match 'BLOCKED_UPSTREAM_SOURCE') 'down must report dirty upstream source as BLOCKED_UPSTREAM_SOURCE'
    } finally {
        if (Test-Path -LiteralPath $dirtyMarker) {
            Remove-Item -LiteralPath $dirtyMarker -Force
        }
    }
}

function Invoke-P005V122Script {
    param(
        [Parameter(Mandatory)][string]$Path,
        [AllowEmptyCollection()][string[]]$Arguments = @()
    )

    $powershell = (Get-Command powershell.exe -ErrorAction Stop).Source
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $powershell -NoProfile -ExecutionPolicy Bypass -File $Path @Arguments 2>&1
        return @{ ExitCode = $LASTEXITCODE; Output = ($output -join "`n") }
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
}

function Assert-RenderOnlyExecution {
    $preflightPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1'
    $preflight = Invoke-P005V122Script -Path $preflightPath -Arguments @('-Mode', 'RenderOnly', '-Json')
    Assert-True ($preflight.ExitCode -eq 0) "RenderOnly preflight failed: $($preflight.Output)"
    $preflightResult = $preflight.Output | ConvertFrom-Json
    Assert-True $preflightResult.ok 'RenderOnly preflight must accept a clean locked official upstream source'

    $clusterOnly = Invoke-P005V122Script -Path $preflightPath -Arguments @('-Mode', 'ClusterOnly', '-Json')
    $clusterOnlyJson = $clusterOnly.Output
    $blockedMarker = $clusterOnlyJson.IndexOf("`nBLOCKED_", [StringComparison]::Ordinal)
    if ($blockedMarker -ge 0) {
        $clusterOnlyJson = $clusterOnlyJson.Substring(0, $blockedMarker)
    }
    $clusterOnlyResult = $clusterOnlyJson | ConvertFrom-Json
    Assert-True (@($clusterOnlyResult.blocked | Where-Object { $_ -in @('BLOCKED_RUNTIME_CREDENTIALS', 'BLOCKED_EGRESS_CIDRS') }).Count -eq 0) 'ClusterOnly preflight must not require runtime credentials or egress CIDRs'
    if ([string]$clusterOnlyResult.resource_checks.docker_cgroup_version -eq '1') {
        Assert-True ($clusterOnly.ExitCode -ne 0) 'ClusterOnly preflight must fail when Docker uses cgroup v1'
        Assert-True ('BLOCKED_CGROUP_V1' -in @($clusterOnlyResult.blocked)) 'ClusterOnly preflight must report BLOCKED_CGROUP_V1'
    } else {
        Assert-True ($clusterOnly.ExitCode -eq 0) "ClusterOnly preflight failed: $($clusterOnly.Output)"
        Assert-True $clusterOnlyResult.ok 'ClusterOnly preflight must pass when the Docker daemon exposes cgroup v2'
    }

    $upPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-up.ps1'
    $firstRender = Invoke-P005V122Script -Path $upPath -Arguments @('-Mode', 'RenderOnly')
    Assert-True ($firstRender.ExitCode -eq 0) "initial RenderOnly deployment failed: $($firstRender.Output)"

    $expectedBrowserURLs = @{
        public = 'http://127.0.0.1:18080'
        internal = 'http://127.0.0.1:18082'
    }
    foreach ($zone in @('public', 'internal')) {
        $manifestPath = Join-Path $script:CacheRoot "evidence\p0-05-v1.2.2\$zone-rendered.yaml"
        $manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding utf8
        $podImages = @([regex]::Matches($manifest, '(?m)^\s*image:\s*["'']?(?<reference>[^"''\s]+)["'']?\s*$') | ForEach-Object { $_.Groups['reference'].Value })
        Assert-True ($podImages.Count -ge 8) "$zone rendered manifest did not expose the expected active image inventory"
        foreach ($image in $podImages) {
            Assert-True ($image -match '@sha256:[a-f0-9]{64}$') "$zone rendered image is not digest pinned: $image"
        }
        Assert-True ($manifest -match [regex]::Escape('"base_url": "' + $expectedBrowserURLs[$zone] + '"')) "$zone Element Web must receive its loopback browser URL"
        Assert-True ($manifest -notmatch '"base_url": "http://[^"\r\n]*\.svc\.cluster\.local') "$zone Element Web must not receive an in-cluster DNS name"
        $controllerGatewayPattern = '(?ms)-\s+name:\s+AGENTTEAMS_AI_GATEWAY_URL\s+value:\s+"' +
            [regex]::Escape("http://higress-gateway.haowork-$zone.svc.cluster.local:80") + '"'
        Assert-True ($manifest -match $controllerGatewayPattern) "$zone controller must retain its in-cluster Higress URL"
    }

    $chartSource = Join-Path $script:CacheRoot 'upstream\AgentTeams-v1.2.2\helm\agentteams'
    $chartCache = Join-Path $script:CacheRoot 'helm\agentteams-v1.2.2'
    $chartYaml = Join-Path $chartCache 'Chart.yaml'
    Assert-True (Test-Path -LiteralPath $chartYaml -PathType Leaf) 'RenderOnly did not materialize the chart cache'
    $originalBytes = [IO.File]::ReadAllBytes($chartYaml)
    $tamperPath = Join-Path $chartCache 'templates\p0-05-v122-tampered.yaml'
    $marker = "p0-05-v122-cache-tamper-$([guid]::NewGuid().ToString('N'))"
    try {
        Add-Content -LiteralPath $chartYaml -Value "# $marker" -Encoding utf8
        @"
apiVersion: v1
kind: ConfigMap
metadata:
  name: $marker
"@ | Set-Content -LiteralPath $tamperPath -Encoding utf8
        Assert-True ((Get-Content -LiteralPath $chartYaml -Raw -Encoding utf8) -match $marker) 'could not prepare chart-cache tamper fixture'
        Assert-True (Test-Path -LiteralPath $tamperPath -PathType Leaf) 'could not prepare extra chart-cache tamper fixture'

        $secondRender = Invoke-P005V122Script -Path $upPath -Arguments @('-Mode', 'RenderOnly')
        Assert-True ($secondRender.ExitCode -eq 0) "RenderOnly after chart-cache tamper failed: $($secondRender.Output)"
        Assert-True ((Get-Content -LiteralPath $chartYaml -Raw -Encoding utf8) -notmatch $marker) 'tampered chart cache was reused after the locked source check'
        Assert-True (-not (Test-Path -LiteralPath $tamperPath -PathType Leaf)) 'extra tampered chart-cache file survived the locked source rebuild'

        $sourceFiles = Get-ChildItem -LiteralPath $chartSource -Recurse -File | ForEach-Object {
            $_.FullName.Substring($chartSource.Length).TrimStart([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
        }
        foreach ($relativePath in $sourceFiles) {
            $sourceFile = Join-Path $chartSource $relativePath
            $cacheFile = Join-Path $chartCache $relativePath
            Assert-True (Test-Path -LiteralPath $cacheFile -PathType Leaf) "rebuilt chart cache is missing source file: $relativePath"
            $sourceHash = (Get-FileHash -LiteralPath $sourceFile -Algorithm SHA256).Hash
            $cacheHash = (Get-FileHash -LiteralPath $cacheFile -Algorithm SHA256).Hash
            Assert-True ($sourceHash -eq $cacheHash) "rebuilt chart cache file hash differs from source: $relativePath"
        }
    } finally {
        if (Test-Path -LiteralPath $chartYaml) {
            $currentText = Get-Content -LiteralPath $chartYaml -Raw -Encoding utf8
            if ($currentText -match $marker) {
                [IO.File]::WriteAllBytes($chartYaml, $originalBytes)
            }
        }
        if (Test-Path -LiteralPath $tamperPath) {
            Remove-Item -LiteralPath $tamperPath -Force
        }
    }
}

function Assert-ConcurrentRenderOnlyExecution {
    $upPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-up.ps1'
    $powershell = (Get-Command powershell.exe -ErrorAction Stop).Source
    $runId = [guid]::NewGuid().ToString('N')
    $processes = @()
    $passed = $false
    $chartCacheParent = Join-Path $script:CacheRoot 'helm'
    $priorTransientCaches = @(
        Get-ChildItem -LiteralPath $chartCacheParent -Directory -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -match '^agentteams-v1\.2\.2\.(stage|backup)-' } |
            Select-Object -ExpandProperty Name
    )
    try {
        foreach ($index in 1..2) {
            $stdoutPath = Join-Path $script:CacheRoot "tmp\p0-05-v122-concurrent-$runId-$index.stdout.log"
            $stderrPath = Join-Path $script:CacheRoot "tmp\p0-05-v122-concurrent-$runId-$index.stderr.log"
            $startInfo = New-Object System.Diagnostics.ProcessStartInfo
            $startInfo.FileName = $powershell
            $startInfo.Arguments = '-NoProfile -ExecutionPolicy Bypass -File "{0}" -Mode RenderOnly' -f $upPath
            $startInfo.WorkingDirectory = $script:WorktreeRoot
            $startInfo.UseShellExecute = $false
            $startInfo.RedirectStandardOutput = $true
            $startInfo.RedirectStandardError = $true

            $process = New-Object System.Diagnostics.Process
            $process.StartInfo = $startInfo
            Assert-True $process.Start() "could not start concurrent RenderOnly process $index"
            $processes += [pscustomobject]@{
                Index = $index
                Process = $process
                StdoutTask = $process.StandardOutput.ReadToEndAsync()
                StderrTask = $process.StandardError.ReadToEndAsync()
                StdoutPath = $stdoutPath
                StderrPath = $stderrPath
            }
        }

        foreach ($entry in $processes) {
            $entry.Process.WaitForExit()
            $entry.StdoutTask.Wait()
            $entry.StderrTask.Wait()
            $stdout = $entry.StdoutTask.Result
            $stderr = $entry.StderrTask.Result
            $exitCode = $entry.Process.ExitCode
            $stdout | Set-Content -LiteralPath $entry.StdoutPath -Encoding utf8
            $stderr | Set-Content -LiteralPath $entry.StderrPath -Encoding utf8
            $output = @($stdout, $stderr) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
            Assert-True ($exitCode -eq 0) "concurrent RenderOnly process $($entry.Index) failed with exit $exitCode. Diagnostics: $($entry.StdoutPath), $($entry.StderrPath). Output: $($output -join "`n")"
        }

        $chartCache = Join-Path $chartCacheParent 'agentteams-v1.2.2'
        Assert-True (Test-Path -LiteralPath (Join-Path $chartCache 'Chart.yaml') -PathType Leaf) 'concurrent RenderOnly did not publish a complete Chart cache'
        Assert-True (Test-Path -LiteralPath (Join-Path $chartCache 'charts\higress-2.2.1.tgz') -PathType Leaf) 'concurrent RenderOnly did not retain the resolved Higress dependency'
        $newTransientCaches = @(
            Get-ChildItem -LiteralPath $chartCacheParent -Directory -ErrorAction SilentlyContinue |
                Where-Object {
                    $_.Name -match '^agentteams-v1\.2\.2\.(stage|backup)-' -and
                    $_.Name -notin $priorTransientCaches
                } |
                Select-Object -ExpandProperty Name
        )
        Assert-True ($newTransientCaches.Count -eq 0) "concurrent RenderOnly left incomplete chart-cache directories: $($newTransientCaches -join ', ')"
        $passed = $true
    } finally {
        foreach ($entry in $processes) {
            if ($null -ne $entry.Process) {
                if (-not $entry.Process.HasExited) { $entry.Process.WaitForExit() }
                $entry.Process.Dispose()
            }
            if ($passed) {
                Remove-P005V122TestPaths -Paths @($entry.StdoutPath, $entry.StderrPath)
            }
        }
    }
}

function Assert-CleanupWithoutClusterExecution {
    $kind = Join-Path $script:RepoRoot '.tools\bin\kind.exe'
    Assert-True (Test-Path -LiteralPath $kind -PathType Leaf) 'project-local Kind executable is required for the cleanup no-cluster test'
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $clusters = @(& $kind get clusters 2>&1)
        $kindQueryExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    Assert-True ($kindQueryExitCode -eq 0) 'could not query Kind clusters for the cleanup no-cluster test'
    Assert-True (@($clusters | Where-Object { $_.ToString().Trim() -eq 'haowork-p005-v122' }).Count -eq 0) 'cleanup no-cluster test requires no haowork-p005-v122 Kind cluster'

    $downPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-down.ps1'
    $cleanup = Invoke-P005V122Script -Path $downPath -Arguments @()
    Assert-True ($cleanup.ExitCode -eq 0) "cleanup must be idempotent when the named Kind cluster is absent: $($cleanup.Output)"
    Assert-True ($cleanup.Output -match 'cleanup skipped') 'cleanup must report that no named Kind cluster exists instead of claiming resource deletion'
}

function Assert-DockerStorageEvidence {
    $preflightPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1'
    $preflight = Invoke-P005V122Script -Path $preflightPath -Arguments @('-Mode', 'LocalDualNamespace', '-Json')
    $evidencePath = Join-Path $script:CacheRoot 'evidence\p0-05-v1.2.2\preflight.json'
    Assert-True (Test-Path -LiteralPath $evidencePath -PathType Leaf) 'local preflight must write non-secret storage evidence before returning blocked states'
    $evidence = Get-Content -LiteralPath $evidencePath -Raw -Encoding utf8 | ConvertFrom-Json
    $storage = $evidence.resource_checks.docker_storage
    Assert-True ($null -ne $storage) 'local preflight must report Docker Desktop storage evidence'
    Assert-True ($storage.status -eq 'READY') "Docker Desktop storage evidence is not ready: $($storage.status)"
    Assert-True ([string]$storage.configured_directory -match '^[Ee]:\\') 'Docker Desktop configured storage must be on E drive'
    Assert-True ([string]$storage.vhdx_path -match 'ext4\.vhdx$') 'Docker Desktop storage evidence must point to an ext4.vhdx file'
    Assert-True ([bool]$storage.vhdx_exists) 'Docker Desktop configured ext4.vhdx must exist'
}

function Assert-BrowserEndpointBlocker {
    $preflightPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1'
    $preflight = Invoke-P005V122Script -Path $preflightPath -Arguments @('-Mode', 'LocalDualNamespace', '-RequireBrowserEndpoint', '-Json')
    Assert-True ($preflight.ExitCode -ne 0) 'post-install preflight must fail when its controlled browser endpoints are not available'

    $evidencePath = Join-Path $script:CacheRoot 'evidence\p0-05-v1.2.2\preflight.json'
    Assert-True (Test-Path -LiteralPath $evidencePath -PathType Leaf) 'post-install preflight must record browser endpoint evidence'
    $evidence = Get-Content -LiteralPath $evidencePath -Raw -Encoding utf8 | ConvertFrom-Json
    Assert-True (@($evidence.blocked) -contains 'BLOCKED_BROWSER_ENDPOINT') 'missing local port-forwards must be reported as BLOCKED_BROWSER_ENDPOINT'
    $endpoints = @($evidence.resource_checks.browser_endpoints)
    Assert-True ($endpoints.Count -eq 2) 'post-install preflight must report both zone browser endpoints'
    foreach ($endpoint in $endpoints) {
        Assert-True ($endpoint.url -match '^http://127\.0\.0\.1:1808[02]$') 'browser endpoint must be an explicit loopback URL'
    }
}

function Assert-UnmanagedBrowserListenerBlocked {
    $listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Parse('127.0.0.1'), 18080)
    try {
        $listener.Start()
        $preflightPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1'
        $preflight = Invoke-P005V122Script -Path $preflightPath -Arguments @('-Mode', 'LocalDualNamespace', '-RequireBrowserEndpoint', '-Json')
        Assert-True ($preflight.ExitCode -ne 0) 'preflight must fail when a browser endpoint is occupied by an unrelated listener'

        $evidencePath = Join-Path $script:CacheRoot 'evidence\p0-05-v1.2.2\preflight.json'
        $evidence = Get-Content -LiteralPath $evidencePath -Raw -Encoding utf8 | ConvertFrom-Json
        $publicEndpoint = @($evidence.resource_checks.browser_endpoints | Where-Object { $_.zone -eq 'public' })
        Assert-True ($publicEndpoint.Count -eq 1) 'preflight must record the public browser endpoint state'
        Assert-True (-not [bool]$publicEndpoint[0].listening) 'an unrelated listener must not satisfy the managed browser endpoint check'
        Assert-True (@($evidence.blocked) -contains 'BLOCKED_BROWSER_ENDPOINT') 'unmanaged browser listener must keep browser acceptance blocked'
    } finally {
        $listener.Stop()
    }
}

function Assert-UnreachableRuntimeProviderBlocked {
    $previous = @{}
    $names = @(
        'HAOWORK_P005_PUBLIC_LLM_API_KEY',
        'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
        'HAOWORK_P005_INTERNAL_LLM_API_KEY',
        'HAOWORK_P005_INTERNAL_LLM_BASE_URL',
        'HAOWORK_P005_PUBLIC_EGRESS_CIDRS',
        'HAOWORK_P005_INTERNAL_EGRESS_CIDRS'
    )
    foreach ($name in $names) {
        $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    }
    try {
        $env:HAOWORK_P005_PUBLIC_LLM_API_KEY = 'test-only-key'
        $env:HAOWORK_P005_PUBLIC_LLM_BASE_URL = 'http://127.0.0.1:1/v1'
        $env:HAOWORK_P005_INTERNAL_LLM_API_KEY = 'test-only-key'
        $env:HAOWORK_P005_INTERNAL_LLM_BASE_URL = 'http://127.0.0.1:1/v1'
        $env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS = '127.0.0.0/8'
        $env:HAOWORK_P005_INTERNAL_EGRESS_CIDRS = '127.0.0.0/8'
        $preflightPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1'
        $preflight = Invoke-P005V122Script -Path $preflightPath -Arguments @('-Mode', 'LocalDualNamespace', '-Json')
        Assert-True ($preflight.ExitCode -ne 0) 'real preflight must fail for an unreachable runtime provider endpoint'
        $evidencePath = Join-Path $script:CacheRoot 'evidence\p0-05-v1.2.2\preflight.json'
        $evidence = Get-Content -LiteralPath $evidencePath -Raw -Encoding utf8 | ConvertFrom-Json
        Assert-True (@($evidence.blocked) -contains 'BLOCKED_RUNTIME_PROVIDER_ENDPOINT') 'unreachable provider must be an explicit blocked state'
        Assert-True ([bool]$evidence.resource_checks.runtime_provider.checked) 'preflight evidence must record that the runtime provider was checked'
        Assert-True (-not [bool]$evidence.resource_checks.runtime_provider.ready) 'preflight evidence must record the failed runtime provider result without endpoint details'
        Assert-True (($evidence | ConvertTo-Json -Depth 8) -notmatch 'test-only-key|127\.0\.0\.1:1(?:/|"|,|\s|$)') 'preflight evidence must not retain runtime credential or provider URL fixtures'
    } finally {
        foreach ($name in $names) {
            [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
        }
    }
}

function Assert-SpecialCharacterRuntimeValueSerialization {
    $upPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-up.ps1'
    $upText = Get-FileText $upPath
    $functionStart = $upText.IndexOf('function ConvertTo-P005V122YamlScalar', [System.StringComparison]::Ordinal)
    $functionEnd = $upText.IndexOf('function Get-P005V122RenderOnlyValues', [System.StringComparison]::Ordinal)
    Assert-True ($functionStart -ge 0 -and $functionEnd -gt $functionStart) 'runtime YAML scalar serializer must remain independently inspectable'
    $runtimeFunctions = $upText.Substring($functionStart, $functionEnd - $functionStart)
    $fixturePath = Join-Path $script:CacheRoot ("tmp\p0-05-v122-runtime-values-$([guid]::NewGuid().ToString('N')).ps1")
    try {
        ($runtimeFunctions + @"
`$script:HAOWORK_P005_PUBLIC_LLM_API_KEY = 'key""with\\slash' + "`n" + 'line-two'
`$script:HAOWORK_P005_PUBLIC_LLM_BASE_URL = 'https://provider.example/v1?quote=""yes'
function Get-Item {
    param([string]`$Path)
    if (`$Path -eq 'Env:HAOWORK_P005_PUBLIC_LLM_API_KEY') { return [pscustomobject]@{ Value = `$script:HAOWORK_P005_PUBLIC_LLM_API_KEY } }
    if (`$Path -eq 'Env:HAOWORK_P005_PUBLIC_LLM_BASE_URL') { return [pscustomobject]@{ Value = `$script:HAOWORK_P005_PUBLIC_LLM_BASE_URL } }
    Microsoft.PowerShell.Management\Get-Item @PSBoundParameters
}
Get-P005V122RuntimeValues -Zone public
"@) | Set-Content -LiteralPath $fixturePath -Encoding utf8
        $result = Invoke-P005V122Script -Path $fixturePath
        Assert-True ($result.ExitCode -eq 0) "runtime YAML scalar serialization failed for quote/backslash/newline input: $($result.Output)"
        Assert-True ($result.Output -match 'llmApiKey:\s+"') 'runtime API key must remain a quoted YAML scalar'
        Assert-True ($result.Output -match 'llmBaseUrl:\s+"') 'runtime base URL must remain a quoted YAML scalar'
        Assert-True ($result.Output -notmatch '(?m)^line-two\s*:') 'runtime newline must not inject a top-level YAML key'
    } finally {
        Remove-P005V122TestPaths -Paths @($fixturePath)
    }
}

function Assert-PreflightEvidenceLockExecution {
    $preflightPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-preflight.ps1'
    $powershell = (Get-Command powershell.exe -ErrorAction Stop).Source
    $runId = [guid]::NewGuid().ToString('N')
    $processes = @()
    try {
        foreach ($clusterName in @("p005-preflight-a-$runId", "p005-preflight-b-$runId")) {
            $startInfo = New-Object System.Diagnostics.ProcessStartInfo
            $startInfo.FileName = $powershell
            $startInfo.Arguments = '-NoProfile -ExecutionPolicy Bypass -File "{0}" -Mode RenderOnly -ClusterName {1} -Json' -f $preflightPath, $clusterName
            $startInfo.WorkingDirectory = $script:WorktreeRoot
            $startInfo.UseShellExecute = $false
            $startInfo.RedirectStandardOutput = $true
            $startInfo.RedirectStandardError = $true
            $process = New-Object System.Diagnostics.Process
            $process.StartInfo = $startInfo
            Assert-True $process.Start() "could not start concurrent preflight for $clusterName"
            $processes += [pscustomobject]@{
                ClusterName = $clusterName
                Process = $process
                StdoutTask = $process.StandardOutput.ReadToEndAsync()
                StderrTask = $process.StandardError.ReadToEndAsync()
            }
        }
        foreach ($entry in $processes) {
            $entry.Process.WaitForExit()
            $entry.StdoutTask.Wait()
            $entry.StderrTask.Wait()
            Assert-True ($entry.Process.ExitCode -eq 0) "concurrent preflight failed for $($entry.ClusterName): $($entry.StdoutTask.Result) $($entry.StderrTask.Result)"
        }
        $evidencePath = Join-Path $script:CacheRoot 'evidence\p0-05-v1.2.2\preflight.json'
        $evidence = Get-Content -LiteralPath $evidencePath -Raw -Encoding utf8 | ConvertFrom-Json
        Assert-True ($evidence.mode -eq 'RenderOnly') 'atomic shared preflight evidence must remain a complete RenderOnly JSON document'
        Assert-True ($evidence.cluster_name -match "^p005-preflight-[ab]-$runId$") 'shared preflight evidence must be published by a complete locked invocation'
    } finally {
        foreach ($entry in $processes) {
            if ($null -ne $entry.Process) {
                if (-not $entry.Process.HasExited) { $entry.Process.WaitForExit() }
                $entry.Process.Dispose()
            }
        }
    }
}

function Assert-ClusterNamePathSafety {
    $upPath = Join-Path $script:WorktreeRoot 'scripts\p0-05-v122-up.ps1'
    $unsafeClusterName = '..\p0-05-v122-path-escape'
    $result = Invoke-P005V122Script -Path $upPath -Arguments @('-Mode', 'RenderOnly', '-ClusterName', $unsafeClusterName)
    Assert-True ($result.ExitCode -ne 0) 'up must reject a cluster name that can escape project cache paths'
    Assert-True ($result.Output -match 'BLOCKED_KIND_CLUSTER_NAME') 'unsafe cluster name must return BLOCKED_KIND_CLUSTER_NAME'
}

Assert-LockContract
Assert-OfficialValues $script:PublicValuesPath 'haowork-public' 'haowork-public-agentteams' 'haowork-public-artifacts' 'http://127.0.0.1:18080'
Assert-OfficialValues $script:InternalValuesPath 'haowork-internal' 'haowork-internal-agentteams' 'haowork-internal-artifacts' 'http://127.0.0.1:18082'
Assert-NetworkPolicy $script:PublicPolicyPath 'haowork-public' 'public'
Assert-NetworkPolicy $script:InternalPolicyPath 'haowork-internal' 'internal'
Assert-DeploymentScripts
Assert-OfficialSourceGate
Assert-RenderOnlyExecution
Assert-ConcurrentRenderOnlyExecution
Assert-CleanupWithoutClusterExecution
Assert-DockerStorageEvidence
Assert-BrowserEndpointBlocker
Assert-UnmanagedBrowserListenerBlocked
Assert-UnreachableRuntimeProviderBlocked
Assert-SpecialCharacterRuntimeValueSerialization
Assert-PreflightEvidenceLockExecution
Assert-ClusterNamePathSafety

$publicText = Get-FileText $script:PublicValuesPath
$internalText = Get-FileText $script:InternalValuesPath
foreach ($sharedValue in @('haowork-public', 'haowork-public-artifacts', 'haowork-public-runtime-env', 'haowork-public.matrix.local')) {
    Assert-True ($internalText -notmatch [regex]::Escape($sharedValue)) "internal values must not reuse public identity: $sharedValue"
}
foreach ($sharedValue in @('haowork-internal', 'haowork-internal-artifacts', 'haowork-internal-runtime-env', 'haowork-internal.matrix.local')) {
    Assert-True ($publicText -notmatch [regex]::Escape($sharedValue)) "public values must not reuse internal identity: $sharedValue"
}

"P0-05 AgentTeams v1.2.2 deployment contract checks passed. Cache root: $script:CacheRoot"
