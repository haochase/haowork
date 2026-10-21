[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Zone,
    [Parameter(Mandatory)][string]$ClusterName
)

$ErrorActionPreference = 'Stop'

function Get-P005V122BrowserSpec {
    param([Parameter(Mandatory)][string]$Zone)
    switch -CaseSensitive ($Zone) {
        'public' { return [ordered]@{ Zone = 'public'; Namespace = 'haowork-public'; ServiceName = 'haowork-public-agentteams-element-web'; LocalPort = 18080; RemotePort = 8080 } }
        'internal' { return [ordered]@{ Zone = 'internal'; Namespace = 'haowork-internal'; ServiceName = 'haowork-internal-agentteams-element-web'; LocalPort = 18082; RemotePort = 8080 } }
        default { throw 'BLOCKED_BROWSER_ZONE' }
    }
}

function Assert-P005V122ClusterName {
    param([Parameter(Mandatory)][string]$Name)
    if ($Name -notmatch '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$') { throw 'BLOCKED_KIND_CLUSTER_NAME' }
    return $Name
}

function Resolve-P005V122TrustedExecutable {
    param(
        [Parameter(Mandatory)][string]$RepoRoot,
        [Parameter(Mandatory)][ValidateSet('kind', 'kubectl')][string]$Name
    )
    $candidate = Join-Path $RepoRoot ".tools\bin\$Name.exe"
    if ([IO.Path]::IsPathRooted($candidate) -and (Test-Path -LiteralPath $candidate -PathType Leaf)) { return [IO.Path]::GetFullPath($candidate) }
    throw "BLOCKED_$($Name.ToUpperInvariant())"
}

function Get-P005V122BrowserRepoRoot {
    param([string]$WorktreeRoot)
    if ([string]::IsNullOrWhiteSpace($WorktreeRoot)) { $WorktreeRoot = Split-Path -Parent $PSScriptRoot }
    $WorktreeRoot = [IO.Path]::GetFullPath($WorktreeRoot)
    $gitMarker = Join-Path $worktreeRoot '.git'
    if (Test-Path -LiteralPath $gitMarker -PathType Container) {
        if (-not (Test-Path -LiteralPath (Join-Path $worktreeRoot '.haowork') -PathType Container) -and
            -not (Test-Path -LiteralPath (Join-Path $worktreeRoot '.tools\bin') -PathType Container)) { throw 'BLOCKED_GIT_WORKTREE' }
        return $worktreeRoot
    }
    if (-not (Test-Path -LiteralPath $gitMarker -PathType Leaf)) { throw 'BLOCKED_GIT_WORKTREE' }
    $marker = Get-Content -LiteralPath $gitMarker -Raw -Encoding ascii
    if ($marker.Trim() -notmatch '^gitdir:\s*(?<path>[^\r\n]+)$') { throw 'BLOCKED_GIT_WORKTREE' }
    $markerPath = $Matches['path'].Trim()
    $gitDir = if ([IO.Path]::IsPathRooted($markerPath)) {
        [IO.Path]::GetFullPath($markerPath)
    } else {
        [IO.Path]::GetFullPath((Join-Path $WorktreeRoot $markerPath))
    }
    if (-not (Test-Path -LiteralPath $gitDir -PathType Container)) { throw 'BLOCKED_GIT_WORKTREE' }
    $commonGitDir = [IO.Path]::GetFullPath((Join-Path $gitDir '..\..'))
    if (-not (Test-Path -LiteralPath $commonGitDir -PathType Container)) { throw 'BLOCKED_GIT_WORKTREE' }
    $repoRoot = Split-Path -Parent $commonGitDir
    if ([IO.Path]::GetFullPath((Join-Path $repoRoot '.git')) -ine $commonGitDir -or
        (-not (Test-Path -LiteralPath (Join-Path $repoRoot '.haowork') -PathType Container) -and
         -not (Test-Path -LiteralPath (Join-Path $repoRoot '.tools\bin') -PathType Container))) { throw 'BLOCKED_GIT_WORKTREE' }
    return $repoRoot
}

function Get-P005V122BrowserOwnershipPath {
    param([Parameter(Mandatory)][string]$CacheRoot, [Parameter(Mandatory)][string]$ClusterName)
    return Join-Path $CacheRoot "kind\$ClusterName.ownership.json"
}

function Read-P005V122BrowserOwnership {
    param([Parameter(Mandatory)][string]$CacheRoot, [Parameter(Mandatory)][string]$ClusterName)
    $path = Get-P005V122BrowserOwnershipPath -CacheRoot $CacheRoot -ClusterName $ClusterName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'BLOCKED_CLUSTER_OWNERSHIP' }
    try {
        $ownership = Get-Content -LiteralPath $path -Raw -Encoding utf8 | ConvertFrom-Json
        if ($ownership.schema_version -ne 1 -or [string]::IsNullOrWhiteSpace([string]$ownership.cluster_name) -or
            [string]::IsNullOrWhiteSpace([string]$ownership.cluster_identity) -or [string]::IsNullOrWhiteSpace([string]$ownership.kube_context)) { throw 'invalid ownership' }
        return $ownership
    } catch { throw 'BLOCKED_CLUSTER_OWNERSHIP' }
}

function Invoke-P005V122BrowserKubectl {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$CacheDirectory,
        [Parameter(Mandatory)][string[]]$Arguments
    )
    & $Kubectl --kubeconfig $env:KUBECONFIG --cache-dir $CacheDirectory --request-timeout=10s @Arguments
}

function Get-P005V122BrowserClusterIdentity {
    param([Parameter(Mandatory)][string]$Kubectl, [Parameter(Mandatory)][string]$CacheDirectory)
    $output = @(Invoke-P005V122BrowserKubectl -Kubectl $Kubectl -CacheDirectory $CacheDirectory -Arguments @('get', 'namespace', 'kube-system', '-o', 'jsonpath={.metadata.uid}'))
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($output -join ''))) { throw 'BLOCKED_CLUSTER_IDENTITY' }
    return ($output -join '').Trim()
}

function Get-P005V122BrowserKubeContext {
    param([Parameter(Mandatory)][string]$Kubectl, [Parameter(Mandatory)][string]$CacheDirectory)
    $output = @(Invoke-P005V122BrowserKubectl -Kubectl $Kubectl -CacheDirectory $CacheDirectory -Arguments @('config', 'current-context'))
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($output -join ''))) { throw 'BLOCKED_CLUSTER_IDENTITY' }
    return ($output -join '').Trim()
}

function Get-P005V122BrowserKindClusters {
    param([Parameter(Mandatory)][string]$Kind)
    $clusters = @(& $Kind get clusters 2>$null)
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_KIND_QUERY' }
    return @($clusters | ForEach-Object { $_.ToString().Trim() } | Where-Object { $_ })
}

function Assert-P005V122BrowserClusterOwnership {
    param(
        [Parameter(Mandatory)][object]$Ownership,
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][string]$ClusterIdentity,
        [Parameter(Mandatory)][string]$KubeContext
    )
    if ($Ownership.schema_version -ne 1 -or [string]$Ownership.cluster_name -cne $ClusterName -or
        [string]$Ownership.cluster_identity -cne $ClusterIdentity -or [string]$Ownership.kube_context -cne $KubeContext) { throw 'BLOCKED_CLUSTER_OWNERSHIP' }
}

function Test-P005V122BrowserReadyEndpoint {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$CacheDirectory,
        [Parameter(Mandatory)][string]$Namespace,
        [Parameter(Mandatory)][string]$ServiceName,
        [int]$DeadlineSeconds = 30
    )
    $deadline = [DateTime]::UtcNow.AddSeconds($DeadlineSeconds)
    do {
        $json = @(Invoke-P005V122BrowserKubectl -Kubectl $Kubectl -CacheDirectory $CacheDirectory -Arguments @('get', 'endpointslices.discovery.k8s.io', '--selector', "kubernetes.io/service-name=$ServiceName", '--namespace', $Namespace, '-o', 'json'))
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace(($json -join "`n"))) {
            try { $value = ($json -join "`n") | ConvertFrom-Json; if (@($value.items | Where-Object { @($_.endpoints | Where-Object { @($_.addresses).Count -gt 0 -and $_.conditions.ready -eq $true }).Count -gt 0 }).Count -gt 0) { return $true } } catch { }
        }
        if ([DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 250 }
    } while ([DateTime]::UtcNow -lt $deadline)
    return $false
}

$spec = Get-P005V122BrowserSpec -Zone $Zone
$ClusterName = Assert-P005V122ClusterName -Name $ClusterName
$repoRoot = Get-P005V122BrowserRepoRoot
if ([IO.Path]::GetPathRoot($repoRoot) -ine 'E:\') { throw 'BLOCKED_E_DRIVE_CACHE' }
$cacheRoot = Join-Path $repoRoot '.haowork\cache'
$kindRoot = Join-Path $cacheRoot 'kind'
$kubectlCache = Join-Path $kindRoot "$ClusterName.kubectl-cache"
New-Item -ItemType Directory -Force $kindRoot, $kubectlCache | Out-Null
$env:KUBECONFIG = Join-Path $kindRoot "$ClusterName.kubeconfig"
if ([IO.Path]::GetPathRoot($env:KUBECONFIG) -ine 'E:\' -or [IO.Path]::GetPathRoot($kubectlCache) -ine 'E:\') { throw 'BLOCKED_E_DRIVE_KUBECONFIG' }

$kind = Resolve-P005V122TrustedExecutable -RepoRoot $repoRoot -Name kind
$kubectl = Resolve-P005V122TrustedExecutable -RepoRoot $repoRoot -Name kubectl
$clusters = Get-P005V122BrowserKindClusters -Kind $kind
if (@($clusters | Where-Object { $_.ToString().Trim() -ceq $ClusterName }).Count -ne 1) { throw 'BLOCKED_CLUSTER_MISSING' }
& $kind export kubeconfig --name $ClusterName --kubeconfig $env:KUBECONFIG | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_KIND_KUBECONFIG' }

$ownership = Read-P005V122BrowserOwnership -CacheRoot $cacheRoot -ClusterName $ClusterName
$identity = Get-P005V122BrowserClusterIdentity -Kubectl $kubectl -CacheDirectory $kubectlCache
$context = Get-P005V122BrowserKubeContext -Kubectl $kubectl -CacheDirectory $kubectlCache
Assert-P005V122BrowserClusterOwnership -Ownership $ownership -ClusterName $ClusterName -ClusterIdentity $identity -KubeContext $context
if (-not (Test-P005V122BrowserReadyEndpoint -Kubectl $kubectl -CacheDirectory $kubectlCache -Namespace $spec.Namespace -ServiceName $spec.ServiceName)) { throw "BLOCKED_BROWSER_ENDPOINT_$($Zone.ToUpperInvariant())" }

Invoke-P005V122BrowserKubectl -Kubectl $kubectl -CacheDirectory $kubectlCache -Arguments @('port-forward', '--address', '127.0.0.1', '--namespace', $spec.Namespace, "service/$($spec.ServiceName)", "$($spec.LocalPort):$($spec.RemotePort)")
if ($LASTEXITCODE -ne 0) { throw "BLOCKED_BROWSER_PORT_FORWARD_$($Zone.ToUpperInvariant())" }
