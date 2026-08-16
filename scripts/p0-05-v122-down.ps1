[CmdletBinding()]
param(
    [string]$ClusterName = 'haowork-p005-v122',
    [switch]$DeleteCluster
)

$ErrorActionPreference = 'Stop'

function Get-P005V122WorktreeRoot { return Split-Path -Parent $PSScriptRoot }

function Assert-P005V122ClusterName {
    param([Parameter(Mandatory)][string]$Name)

    if ($Name -notmatch '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$') {
        throw 'BLOCKED_KIND_CLUSTER_NAME'
    }
    return $Name
}

function Invoke-P005V122WithDeploymentLock {
    param(
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][scriptblock]$Action,
        [int]$TimeoutSeconds = 300
    )

    $mutex = New-Object System.Threading.Mutex($false, "Haowork.P005.AgentTeams.V122.Deployment.$ClusterName")
    $hasLock = $false
    try {
        try {
            $hasLock = $mutex.WaitOne([TimeSpan]::FromSeconds($TimeoutSeconds))
        } catch [System.Threading.AbandonedMutexException] {
            $hasLock = $true
        }
        if (-not $hasLock) { throw 'BLOCKED_DEPLOYMENT_LOCK_TIMEOUT' }
        & $Action
    } finally {
        if ($hasLock) { $mutex.ReleaseMutex() }
        $mutex.Dispose()
    }
}

function Get-P005V122RepoRoot {
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

function Get-P005V122BrowserPortForwardStatePath {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    return Join-Path $CacheRoot "kind\$ClusterName.browser-port-forwards.json"
}

function Assert-P005V122BrowserPortForwardStatePath {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$StatePath
    )

    $kindRoot = [IO.Path]::GetFullPath((Join-Path $CacheRoot 'kind'))
    $resolvedStatePath = [IO.Path]::GetFullPath($StatePath)
    if (-not $resolvedStatePath.StartsWith($kindRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'BLOCKED_BROWSER_PORT_FORWARD_STATE'
    }
}

function Read-P005V122BrowserPortForwardState {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    $statePath = Get-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -ClusterName $ClusterName
    Assert-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -StatePath $statePath
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) { return @() }
    try {
        $state = Get-Content -LiteralPath $statePath -Raw -Encoding utf8 | ConvertFrom-Json
        return @($state.entries)
    } catch {
        throw 'BLOCKED_BROWSER_PORT_FORWARD_STATE'
    }
}

function Get-P005V122ProcessStartTimeUtc {
    param([Parameter(Mandatory)][object]$Process)

    try {
        $creationDate = $Process.CreationDate
        $dateTime = if ($creationDate -is [DateTime]) {
            [DateTime]$creationDate
        } else {
            [System.Management.ManagementDateTimeConverter]::ToDateTime([string]$creationDate)
        }
        return $dateTime.ToUniversalTime().ToString('o')
    } catch {
        return $null
    }
}

function Test-P005V122RunToken {
    param([Parameter(Mandatory)][string]$Value)

    return $Value -match '^[0-9a-f]{32}$'
}

function Test-P005V122ManagedBrowserCacheDirectory {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][string]$Zone,
        [Parameter(Mandatory)][string]$CacheDirectory,
        [Parameter(Mandatory)][string]$RunToken
    )

    if (-not (Test-P005V122RunToken -Value $RunToken)) { return $false }
    try {
        $expected = [IO.Path]::GetFullPath((Join-Path $CacheRoot "kind\$ClusterName.$Zone.browser-port-forward-$RunToken.kubectl-cache"))
        return [IO.Path]::GetFullPath($CacheDirectory) -ieq $expected
    } catch {
        return $false
    }
}

function Test-P005V122ManagedBrowserPortForward {
    param(
        [Parameter(Mandatory)][int]$ProcessId,
        [Parameter(Mandatory)][string]$Namespace,
        [Parameter(Mandatory)][int]$Port,
        [Parameter(Mandatory)][string]$ProcessStartTimeUtc,
        [Parameter(Mandatory)][string]$KubeconfigPath,
        [Parameter(Mandatory)][string]$KubectlPath,
        [Parameter(Mandatory)][string]$CacheDirectory,
        [Parameter(Mandatory)][string]$RunToken
    )

    try {
        $process = Get-CimInstance Win32_Process -Filter "ProcessId = $ProcessId" -ErrorAction Stop
        if ($null -eq $process -or [string]$process.Name -notmatch '(?i)^kubectl(?:\.exe)?$') { return $false }
        if ((Get-P005V122ProcessStartTimeUtc -Process $process) -cne $ProcessStartTimeUtc) { return $false }
        if (-not (Test-P005V122RunToken -Value $RunToken)) { return $false }
        if ([IO.Path]::GetFullPath([string]$process.ExecutablePath) -ine [IO.Path]::GetFullPath($KubectlPath)) { return $false }
        $commandLine = [string]$process.CommandLine
        $namespacePattern = '(?i)(?:^|\s)--namespace(?:\s+|=)"?' + [regex]::Escape($Namespace) + '"?(?=\s|$)'
        $addressPattern = '(?i)(?:^|\s)--address(?:\s+|=)"?127\.0\.0\.1"?(?=\s|$)'
        $portPattern = '(?<!\d)' + [regex]::Escape("$Port:80") + '(?!\d)'
        $kubeconfigPattern = '(?i)(?:^|\s)--kubeconfig(?:\s+|=)"?' + [regex]::Escape([IO.Path]::GetFullPath($KubeconfigPath)) + '"?(?=\s|$)'
        $cacheDirectoryPattern = '(?i)(?:^|\s)--cache-dir(?:\s+|=)"?' + [regex]::Escape([IO.Path]::GetFullPath($CacheDirectory)) + '"?(?=\s|$)'
        return $commandLine -match '(?i)\bport-forward\b' -and
            $commandLine -match $namespacePattern -and
            $commandLine -match $addressPattern -and
            $commandLine -match '(?i)(?:^|\s)(?:service/|svc/)higress-gateway(?=\s|$)' -and
            $commandLine -match $portPattern -and
            $commandLine -match $kubeconfigPattern -and
            $commandLine -match $cacheDirectoryPattern -and
            $CacheDirectory -match [regex]::Escape($RunToken)
    } catch {
        return $false
    }
}

function Stop-P005V122BrowserPortForward {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    $entries = Read-P005V122BrowserPortForwardState -CacheRoot $CacheRoot -ClusterName $ClusterName
    foreach ($entry in $entries) {
        try {
            $processId = [int]$entry.pid
            $zone = [string]$entry.zone
            $namespace = [string]$entry.namespace
            $port = [int]$entry.port
            $processStartTimeUtc = [string]$entry.process_start_time_utc
            $kubeconfigPath = [string]$entry.kubeconfig_path
            $kubectlPath = [string]$entry.kubectl_path
            $cacheDirectory = [string]$entry.cache_directory
            $runToken = [string]$entry.run_token
        } catch {
            throw 'BLOCKED_BROWSER_PORT_FORWARD_STATE'
        }
        $safeCacheDirectory = Test-P005V122ManagedBrowserCacheDirectory -CacheRoot $CacheRoot -ClusterName $ClusterName -Zone $zone -CacheDirectory $cacheDirectory -RunToken $runToken
        if ($safeCacheDirectory -and
            -not [string]::IsNullOrWhiteSpace($processStartTimeUtc) -and
            -not [string]::IsNullOrWhiteSpace($kubeconfigPath) -and
            -not [string]::IsNullOrWhiteSpace($kubectlPath) -and
            (Test-P005V122ManagedBrowserPortForward -ProcessId $processId -Namespace $namespace -Port $port -ProcessStartTimeUtc $processStartTimeUtc -KubeconfigPath $kubeconfigPath -KubectlPath $kubectlPath -CacheDirectory $cacheDirectory -RunToken $runToken)) {
            try {
                Stop-Process -Id $processId -Force -ErrorAction Stop
            } catch {
                throw 'BLOCKED_BROWSER_PORT_FORWARD_STOP'
            }
        }
        if ($safeCacheDirectory -and (Test-Path -LiteralPath $cacheDirectory)) {
            Remove-Item -LiteralPath $cacheDirectory -Recurse -Force -ErrorAction Stop
        }
    }

    $statePath = Get-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -ClusterName $ClusterName
    Assert-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -StatePath $statePath
    if (Test-Path -LiteralPath $statePath) {
        Remove-Item -LiteralPath $statePath -Force -ErrorAction Stop
    }
}

function Get-P005V122DeploymentOwnershipPath {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    return Join-Path $CacheRoot "kind\$ClusterName.ownership.json"
}

function Assert-P005V122DeploymentOwnershipPath {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$OwnershipPath
    )

    $kindRoot = [IO.Path]::GetFullPath((Join-Path $CacheRoot 'kind'))
    $resolvedOwnershipPath = [IO.Path]::GetFullPath($OwnershipPath)
    if (-not $resolvedOwnershipPath.StartsWith($kindRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'BLOCKED_DEPLOYMENT_OWNERSHIP'
    }
}

function Get-P005V122DeploymentOwnership {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    $ownershipPath = Get-P005V122DeploymentOwnershipPath -CacheRoot $CacheRoot -ClusterName $ClusterName
    Assert-P005V122DeploymentOwnershipPath -CacheRoot $CacheRoot -OwnershipPath $ownershipPath
    if (-not (Test-Path -LiteralPath $ownershipPath -PathType Leaf)) { return $null }
    try {
        $ownership = Get-Content -LiteralPath $ownershipPath -Raw -Encoding utf8 | ConvertFrom-Json
        if ($ownership.schema_version -ne 1 -or
            [string]::IsNullOrWhiteSpace([string]$ownership.cluster_name) -or
            [string]::IsNullOrWhiteSpace([string]$ownership.cluster_identity) -or
            [string]::IsNullOrWhiteSpace([string]$ownership.deployment_id)) {
            throw 'invalid ownership payload'
        }
        return $ownership
    } catch {
        throw 'BLOCKED_DEPLOYMENT_OWNERSHIP'
    }
}

function Write-P005V122DeploymentOwnership {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][object]$Ownership
    )

    $ownershipPath = Get-P005V122DeploymentOwnershipPath -CacheRoot $CacheRoot -ClusterName $ClusterName
    Assert-P005V122DeploymentOwnershipPath -CacheRoot $CacheRoot -OwnershipPath $ownershipPath
    $temporaryPath = "$ownershipPath.$([guid]::NewGuid().ToString('N')).tmp"
    try {
        $Ownership | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $temporaryPath -Encoding utf8
        Move-Item -LiteralPath $temporaryPath -Destination $ownershipPath -Force
    } finally {
        if (Test-Path -LiteralPath $temporaryPath) {
            Remove-Item -LiteralPath $temporaryPath -Force -ErrorAction SilentlyContinue
        }
    }
}

function Remove-P005V122DeploymentOwnership {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    $ownershipPath = Get-P005V122DeploymentOwnershipPath -CacheRoot $CacheRoot -ClusterName $ClusterName
    Assert-P005V122DeploymentOwnershipPath -CacheRoot $CacheRoot -OwnershipPath $ownershipPath
    if (Test-Path -LiteralPath $ownershipPath -PathType Leaf) {
        Remove-Item -LiteralPath $ownershipPath -Force -ErrorAction Stop
    }
}

function Get-P005V122KindClusters {
    param([Parameter(Mandatory)][string]$Kind)

    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $clusters = @(& $Kind get clusters 2>&1 | ForEach-Object { $_.ToString().Trim() })
        $kindQueryExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($kindQueryExitCode -ne 0) { throw 'BLOCKED_KIND_QUERY' }
    return @($clusters | Where-Object { $_ -and $_ -ne 'No kind clusters found.' })
}

function Get-P005V122ClusterIdentity {
    param([Parameter(Mandatory)][string]$Kubectl)

    $identity = (& $Kubectl get namespace kube-system -o "jsonpath={.metadata.uid}").Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($identity)) { throw 'BLOCKED_CLUSTER_IDENTITY' }
    return $identity
}

function Get-P005V122KubeContext {
    param([Parameter(Mandatory)][string]$Kubectl)

    $context = (& $Kubectl config current-context).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($context)) { throw 'BLOCKED_CLUSTER_IDENTITY' }
    return $context
}

function Get-P005V122NamespaceObject {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$Namespace
    )

    $namespaceJson = & $Kubectl get namespace $Namespace --ignore-not-found -o json
    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_NAMESPACE_QUERY_$($Namespace.ToUpperInvariant().Replace('-', '_'))" }
    if ([string]::IsNullOrWhiteSpace(($namespaceJson -join "`n"))) { return $null }
    try {
        return (($namespaceJson -join "`n") | ConvertFrom-Json)
    } catch {
        throw "BLOCKED_NAMESPACE_QUERY_$($Namespace.ToUpperInvariant().Replace('-', '_'))"
    }
}

function Test-P005V122ClusterOwnership {
    param(
        [Parameter(Mandatory)][object]$Ownership,
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][string]$ClusterIdentity,
        [Parameter(Mandatory)][string]$KubeContext
    )

    return $Ownership.schema_version -eq 1 -and
        [string]$Ownership.cluster_name -ceq $ClusterName -and
        [string]$Ownership.cluster_identity -ceq $ClusterIdentity -and
        [string]$Ownership.kube_context -ceq $KubeContext -and
        $Ownership.cluster_created_by_haowork -is [bool] -and
        $Ownership.cluster_created_by_haowork -eq $true -and
        [string]$Ownership.deployment_id -match '^[0-9a-f]{32}$'
}

function Test-P005V122NamespaceOwnership {
    param(
        [Parameter(Mandatory)][object]$Ownership,
        [Parameter(Mandatory)][object]$NamespaceObject,
        [Parameter(Mandatory)][string]$Namespace,
        [Parameter(Mandatory)][string]$Zone
    )

    $entries = @($Ownership.namespaces | Where-Object { [string]$_.name -ceq $Namespace })
    if ($entries.Count -ne 1) { return $false }
    $ownerLabel = [string]$NamespaceObject.metadata.labels.'haowork.io/p005-owner'
    $zoneLabel = [string]$NamespaceObject.metadata.labels.'haowork.io/zone'
    return [string]$entries[0].uid -ceq [string]$NamespaceObject.metadata.uid -and
        $ownerLabel -ceq [string]$Ownership.deployment_id -and
        $zoneLabel -ceq $Zone
}

function Assert-P005V122LockedOfficialSource {
    param(
        [Parameter(Mandatory)][string]$WorktreeRoot,
        [Parameter(Mandatory)][string]$RepoRoot
    )

    $commonPath = Join-Path $WorktreeRoot 'scripts\p0-05-v122-common.ps1'
    $lockPath = Join-Path $WorktreeRoot 'deploy\agentteams\v1.2.2\upstream.lock.json'
    $upstreamRoot = Join-Path $RepoRoot '.haowork\cache\upstream\AgentTeams-v1.2.2'
    if (-not (Test-Path -LiteralPath $commonPath -PathType Leaf) -or -not (Test-Path -LiteralPath $lockPath -PathType Leaf)) {
        throw 'BLOCKED_UPSTREAM_CONTRACT'
    }
    . $commonPath
    try {
        $contract = Get-P005V122OfficialContract -ContractPath $lockPath
        Assert-P005V122OfficialContract -Contract $contract
        $sourceIsValid = Invoke-Command -ScriptBlock {
            param($ValidatedContract, $ValidatedUpstreamRoot)
            Test-P005V122OfficialSource -Contract $ValidatedContract -UpstreamRoot $ValidatedUpstreamRoot
        } -ArgumentList $contract, $upstreamRoot
        if (-not [bool]$sourceIsValid) {
            throw 'source did not match lock'
        }
    } catch {
        if ($_.Exception.Message -eq 'AgentTeams v1.2.2 contract lock does not match the required upstream facts') {
            throw 'BLOCKED_UPSTREAM_CONTRACT'
        }
        throw 'BLOCKED_UPSTREAM_SOURCE'
    }
    return $upstreamRoot
}

$ClusterName = Assert-P005V122ClusterName -Name $ClusterName
$worktreeRoot = Get-P005V122WorktreeRoot
$repoRoot = Get-P005V122RepoRoot -WorktreeRoot $worktreeRoot
$cacheRoot = Join-Path $repoRoot '.haowork\cache'
foreach ($name in @('tmp', 'go', 'gomod', 'helm', 'helm\config', 'helm\data', 'kind')) {
    New-Item -ItemType Directory -Force (Join-Path $cacheRoot $name) | Out-Null
}
$env:TEMP = Join-Path $cacheRoot 'tmp'
$env:TMP = $env:TEMP
$env:GOCACHE = Join-Path $cacheRoot 'go'
$env:GOMODCACHE = Join-Path $cacheRoot 'gomod'
$env:HELM_CACHE_HOME = Join-Path $cacheRoot 'helm'
$env:HELM_CONFIG_HOME = Join-Path $cacheRoot 'helm\config'
$env:HELM_DATA_HOME = Join-Path $cacheRoot 'helm\data'
$env:KUBECONFIG = Join-Path $cacheRoot "kind\$ClusterName.kubeconfig"
$helm = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'helm'
$kind = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kind'
$kubectl = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kubectl'
$zones = @(
    @{ Name = 'public'; Namespace = 'haowork-public'; Release = 'haowork-public-agentteams' },
    @{ Name = 'internal'; Namespace = 'haowork-internal'; Release = 'haowork-internal-agentteams' }
)

Invoke-P005V122WithDeploymentLock -ClusterName $ClusterName -Action {
    # This must precede even the no-cluster fast path: down is destructive when
    # state is present and must never act on a dirty or drifted upstream cache.
    $null = Assert-P005V122LockedOfficialSource -WorktreeRoot $worktreeRoot -RepoRoot $repoRoot
    $kindClusters = Get-P005V122KindClusters -Kind $kind
    if (@($kindClusters | Where-Object { $_ -eq $ClusterName }).Count -eq 0) {
        Write-Output 'PASS P0-05 v1.2.2 cleanup skipped because the named Kind cluster does not exist; evidence and Docker Desktop data were preserved.'
        return
    }

    $ownership = Get-P005V122DeploymentOwnership -CacheRoot $cacheRoot -ClusterName $ClusterName
    if ($null -eq $ownership) { throw 'BLOCKED_DEPLOYMENT_OWNERSHIP' }

    & $kind export kubeconfig --name $ClusterName --kubeconfig $env:KUBECONFIG | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_KIND_KUBECONFIG' }
    $clusterIdentity = Get-P005V122ClusterIdentity -Kubectl $kubectl
    $kubeContext = Get-P005V122KubeContext -Kubectl $kubectl
    if (-not (Test-P005V122ClusterOwnership -Ownership $ownership -ClusterName $ClusterName -ClusterIdentity $clusterIdentity -KubeContext $kubeContext)) {
        throw 'BLOCKED_CLUSTER_OWNERSHIP'
    }

    $existingNamespaces = @()
    foreach ($zone in $zones) {
        $namespaceObject = Get-P005V122NamespaceObject -Kubectl $kubectl -Namespace $zone.Namespace
        if ($null -eq $namespaceObject) { continue }
        if (-not (Test-P005V122NamespaceOwnership -Ownership $ownership -NamespaceObject $namespaceObject -Namespace $zone.Namespace -Zone $zone.Name)) {
            throw "BLOCKED_NAMESPACE_OWNERSHIP_$($zone.Name.ToUpperInvariant())"
        }
        $existingNamespaces += $zone
    }

    # A matching project state file is not enough to stop a process. Verify the
    # currently selected Kind cluster and both namespaces first, so a stale
    # state cannot interrupt a user-owned cluster that reused this name.
    Stop-P005V122BrowserPortForward -CacheRoot $cacheRoot -ClusterName $ClusterName

    foreach ($zone in $zones) {
        & $helm uninstall $zone.Release --namespace $zone.Namespace --ignore-not-found --wait --timeout 5m | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "BLOCKED_HELM_UNINSTALL_$($zone.Name.ToUpperInvariant())" }
    }
    foreach ($zone in $existingNamespaces) {
        & $kubectl delete namespace $zone.Namespace --ignore-not-found | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "BLOCKED_NAMESPACE_DELETE_$($zone.Name.ToUpperInvariant())" }
        & $kubectl wait --for=delete "namespace/$($zone.Namespace)" --timeout=120s | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "BLOCKED_NAMESPACE_DELETE_WAIT_$($zone.Name.ToUpperInvariant())" }
    }

    if ($DeleteCluster) {
        if ($ownership.cluster_created_by_haowork -isnot [bool] -or $ownership.cluster_created_by_haowork -ne $true) { throw 'BLOCKED_CLUSTER_OWNERSHIP' }
        & $kind delete cluster --name $ClusterName | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_KIND_DELETE' }
        Remove-P005V122DeploymentOwnership -CacheRoot $cacheRoot -ClusterName $ClusterName
        Write-Output 'PASS P0-05 v1.2.2 owned releases, namespaces, and Kind cluster were removed; evidence and Docker Desktop data were preserved.'
        return
    }

    $ownership.namespaces = @()
    Write-P005V122DeploymentOwnership -CacheRoot $cacheRoot -ClusterName $ClusterName -Ownership $ownership
    Write-Output 'PASS P0-05 v1.2.2 owned releases and namespaces were removed; the owned Kind cluster, evidence, and Docker Desktop data were preserved.'
}
