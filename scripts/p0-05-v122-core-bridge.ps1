[CmdletBinding()]
param(
    [string]$ClusterName = 'haowork-p005-v122'
)

$ErrorActionPreference = 'Stop'

function Resolve-P005V122CoreTool {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$RepoRoot)

    $local = Join-Path $RepoRoot ".tools\bin\$Name.exe"
    if (Test-Path -LiteralPath $local -PathType Leaf) { return $local }
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -eq $command) { throw "BLOCKED_$($Name.ToUpperInvariant())" }
    return $command.Source
}

function Invoke-P005V122CoreNative {
    param([Parameter(Mandatory)][string]$FilePath, [Parameter(Mandatory)][string[]]$Arguments)

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) { throw "native command failed: $([IO.Path]::GetFileName($FilePath))" }
}

function New-P005V122RandomHex {
    param([int]$Bytes = 32)

    $buffer = New-Object byte[] $Bytes
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $generator.GetBytes($buffer) } finally { $generator.Dispose() }
    return ([BitConverter]::ToString($buffer)).Replace('-', '').ToLowerInvariant()
}

function ConvertFrom-P005V122Base64 {
    param([Parameter(Mandatory)][string]$Value)
    return [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Value))
}

function ConvertFrom-P005V122AppServiceToken {
    param([AllowEmptyString()][string]$EncodedValue)

    try {
        if ([string]::IsNullOrWhiteSpace($EncodedValue)) { throw 'invalid token' }
        $value = ConvertFrom-P005V122Base64 -Value $EncodedValue
        if ([string]::IsNullOrWhiteSpace($value) -or $value.Contains("`n") -or $value.Contains("`r") -or $value.Contains([char]0)) { throw 'invalid token' }
        return $value
    } catch {
        throw 'BLOCKED_MATRIX_APPSERVICE_TOKEN'
    }
}

function Wait-P005V122LocalDeploymentObserved {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$Namespace,
        [Parameter(Mandatory)][string]$Deployment,
        [int]$TimeoutSeconds = 180
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $deploymentJSON = (& $Kubectl get deployment $Deployment -n $Namespace -o json) -join "`n"
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace($deploymentJSON)) {
            try {
                $observed = $deploymentJSON | ConvertFrom-Json
                $replicas = [int]$observed.spec.replicas
                if ($replicas -gt 0 -and [long]$observed.status.observedGeneration -eq [long]$observed.metadata.generation -and
                    [int]$observed.status.updatedReplicas -eq $replicas -and [int]$observed.status.replicas -eq $replicas) {
                    return
                }
            } catch {
            }
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    throw "BLOCKED_LOCAL_DEPLOYMENT_OBSERVATION_$($Namespace.ToUpperInvariant().Replace('-', '_'))_$($Deployment.ToUpperInvariant().Replace('-', '_'))"
}

function Set-P005V122PrivateAcl {
    param(
        [Parameter(Mandatory)][string]$Path,
        [switch]$Directory
    )

    $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $inheritance = if ($Directory) { '(OI)(CI)' } else { '' }
    & icacls.exe $Path /inheritance:r /grant:r "*$currentSid`:${inheritance}F" "*S-1-5-18:${inheritance}F" "*S-1-5-32-544:${inheritance}F" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CORE_BRIDGE_SECRET_ACL' }
    try {
        $acl = Get-Acl -LiteralPath $Path -ErrorAction Stop
        $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
        $allowedSids = @($currentSid, 'S-1-5-18', 'S-1-5-32-544')
        $unexpectedAllows = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]) | Where-Object {
            $_.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and $_.IdentityReference.Value -notin $allowedSids
        })
        if (-not $acl.AreAccessRulesProtected -or $owner -cne $currentSid -or $unexpectedAllows.Count -ne 0) {
            throw 'invalid private ACL'
        }
    } catch {
        throw 'BLOCKED_CORE_BRIDGE_SECRET_ACL'
    }
}

function Write-P005V122SecretFile {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string[]]$Lines
    )

    $stream = $null
    try {
        if (Test-Path -LiteralPath $Path) { Remove-Item -LiteralPath $Path -Force -ErrorAction Stop }
        $stream = [IO.FileStream]::new($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read, 4096, [IO.FileOptions]::None)
        Set-P005V122PrivateAcl -Path $Path
        $writer = [IO.StreamWriter]::new($stream, [Text.UTF8Encoding]::new($false), 4096, $true)
        try {
            foreach ($line in $Lines) { $writer.WriteLine($line) }
            $writer.Flush()
            $stream.Flush($true)
        } finally {
            $writer.Dispose()
        }
        $stream.Dispose()
        $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
        return $stream
    } catch {
        if ($null -ne $stream) { $stream.Dispose() }
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
        throw 'BLOCKED_CORE_BRIDGE_SECRET_ACL'
    }
}

function Protect-P005V122SecretDirectory {
    param([Parameter(Mandatory)][string]$Path)

    Set-P005V122PrivateAcl -Path $Path -Directory
}

function Invoke-P005V122HigressJSON {
    param(
        [Parameter(Mandatory)][string]$BaseURL,
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Path,
        [AllowNull()][object]$Body,
        [Parameter(Mandatory)][Microsoft.PowerShell.Commands.WebRequestSession]$Session
    )

    $parameters = @{ Method = $Method; Uri = "$BaseURL$Path"; WebSession = $Session; UseBasicParsing = $true }
    if ($null -ne $Body) {
        $parameters.ContentType = 'application/json'
        $parameters.Body = ($Body | ConvertTo-Json -Depth 12 -Compress)
    }
    $response = Invoke-WebRequest @parameters
    if ($response.StatusCode -lt 200 -or $response.StatusCode -ge 300) { throw "Higress Console request failed: $Path" }
    if ([string]::IsNullOrWhiteSpace($response.Content)) { return $null }
    return $response.Content | ConvertFrom-Json
}

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$commonPath = Join-Path $PSScriptRoot 'p0-05-v122-common.ps1'
. $commonPath
$contract = Get-P005V122OfficialContract -ContractPath (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\upstream.lock.json')
$lockedManagerImage = Get-P005V122LockedImageReference -Contract $contract -Name 'manager'
$commonGitDir = (& git -C $worktreeRoot rev-parse --git-common-dir).Trim()
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_GIT_WORKTREE' }
$repoRoot = Split-Path -Parent ([IO.Path]::GetFullPath($commonGitDir))
$cacheRoot = Join-Path $repoRoot '.haowork\cache'
$tempRoot = Join-Path $cacheRoot 'tmp\p0-05-v122-core-bridge'
$imageRoot = Join-Path $cacheRoot 'images\p0-05-v1.2.2'
$runtimeRoot = Join-Path $cacheRoot 'runtime\p0-05-v1.2.2'
foreach ($directory in @($tempRoot, $imageRoot, $runtimeRoot, (Join-Path $cacheRoot 'go\build'), (Join-Path $cacheRoot 'gomod'))) {
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
}
$env:TEMP = $tempRoot
$env:TMP = $tempRoot
$env:GOCACHE = Join-Path $cacheRoot 'go\build'
$env:GOMODCACHE = Join-Path $cacheRoot 'gomod'
$env:KUBECONFIG = Join-Path $cacheRoot "kind\$ClusterName.kubeconfig"

$environmentLoader = Join-Path $PSScriptRoot 'p0-05-v122-env.ps1'
if (-not (Test-Path -LiteralPath $environmentLoader -PathType Leaf)) { throw 'BLOCKED_LOCAL_ENV_LOADER' }
. $environmentLoader
Import-P005V122LocalEnvironment -Path (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\.env.local')
$receiptSkillPath = Join-Path $worktreeRoot 'skills\core\submit_execution_report.yaml'
if (-not (Test-Path -LiteralPath $receiptSkillPath -PathType Leaf) -or
    -not (Get-Content -LiteralPath $receiptSkillPath -Raw -Encoding utf8).Contains('name: submit_execution_report')) {
    throw 'BLOCKED_EXECUTION_RECEIPT_SKILL'
}
$env:HAOWORK_P005_CORE_BRIDGE_RECEIPT_TOOL = 'submit_execution_report'

$kubectl = Resolve-P005V122CoreTool -Name 'kubectl' -RepoRoot $repoRoot
$kind = Resolve-P005V122CoreTool -Name 'kind' -RepoRoot $repoRoot
$docker = Resolve-P005V122CoreTool -Name 'docker' -RepoRoot $repoRoot
$go = Resolve-P005V122CoreTool -Name 'go' -RepoRoot $repoRoot

Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('config', 'use-context', "kind-$ClusterName")

$originalGOOS, $originalGOARCH, $originalCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
try {
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    Invoke-P005V122CoreNative -FilePath $go -Arguments @('build', '-trimpath', '-ldflags=-s -w', '-o', (Join-Path $imageRoot 'haowork-core-bridge'), './cmd/haowork-core-bridge')
    Invoke-P005V122CoreNative -FilePath $go -Arguments @('build', '-trimpath', '-ldflags=-s -w', '-o', (Join-Path $imageRoot 'haowork-mcp'), './cmd/haowork-mcp')
    Invoke-P005V122CoreNative -FilePath $go -Arguments @('build', '-trimpath', '-ldflags=-s -w', '-o', (Join-Path $imageRoot 'haowork-agentteams-binding-controller'), './cmd/haowork-agentteams-binding-controller')
    Invoke-P005V122CoreNative -FilePath $go -Arguments @('build', '-trimpath', '-ldflags=-s -w', '-o', (Join-Path $imageRoot 'haowork-higress-binding-publisher'), './cmd/haowork-higress-binding-publisher')
    Invoke-P005V122CoreNative -FilePath $go -Arguments @('build', '-trimpath', '-ldflags=-s -w', '-o', (Join-Path $imageRoot 'haowork-network-probe'), './cmd/haowork-network-probe')
} finally {
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $originalGOOS, $originalGOARCH, $originalCGO
}

$coreContext = Join-Path $tempRoot 'core-image'
$mcpContext = Join-Path $tempRoot 'mcp-image'
$bindingControllerContext = Join-Path $tempRoot 'binding-controller-image'
$bindingPublisherContext = Join-Path $tempRoot 'binding-publisher-image'
$safeTempRoot = [IO.Path]::GetFullPath($tempRoot).TrimEnd('\') + '\'
foreach ($path in @($coreContext, $mcpContext, $bindingControllerContext, $bindingPublisherContext)) {
    if (-not [IO.Path]::GetFullPath($path).StartsWith($safeTempRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'BLOCKED_CORE_BRIDGE_BUILD_CONTEXT'
    }
}
Remove-Item -LiteralPath $coreContext, $mcpContext, $bindingControllerContext, $bindingPublisherContext -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $coreContext, (Join-Path $mcpContext 'skills'), $bindingControllerContext, $bindingPublisherContext | Out-Null
Copy-Item -LiteralPath (Join-Path $imageRoot 'haowork-core-bridge') -Destination (Join-Path $coreContext 'haowork-core-bridge')
Copy-Item -LiteralPath (Join-Path $imageRoot 'haowork-mcp') -Destination (Join-Path $mcpContext 'haowork-mcp')
Copy-Item -Path (Join-Path $worktreeRoot 'skills\*') -Destination (Join-Path $mcpContext 'skills') -Recurse
Copy-Item -LiteralPath (Join-Path $imageRoot 'haowork-agentteams-binding-controller') -Destination (Join-Path $bindingControllerContext 'haowork-agentteams-binding-controller')
Copy-Item -LiteralPath (Join-Path $imageRoot 'haowork-higress-binding-publisher') -Destination (Join-Path $bindingPublisherContext 'haowork-higress-binding-publisher')
[IO.File]::WriteAllText((Join-Path $coreContext 'Dockerfile'), "FROM scratch`nCOPY haowork-core-bridge /haowork-core-bridge`nUSER 65532:65532`nENTRYPOINT [`"/haowork-core-bridge`"]`n", [Text.UTF8Encoding]::new($false))
[IO.File]::WriteAllText((Join-Path $mcpContext 'Dockerfile'), "FROM scratch`nCOPY haowork-mcp /haowork-mcp`nCOPY skills /opt/haowork/skills`nUSER 65532:65532`nENTRYPOINT [`"/haowork-mcp`"]`n", [Text.UTF8Encoding]::new($false))
[IO.File]::WriteAllText((Join-Path $bindingControllerContext 'Dockerfile'), "FROM scratch`nCOPY haowork-agentteams-binding-controller /haowork-agentteams-binding-controller`nUSER 65532:65532`nENTRYPOINT [`"/haowork-agentteams-binding-controller`"]`n", [Text.UTF8Encoding]::new($false))
[IO.File]::WriteAllText((Join-Path $bindingPublisherContext 'Dockerfile'), "FROM scratch`nCOPY haowork-higress-binding-publisher /haowork-higress-binding-publisher`nUSER 65532:65532`nENTRYPOINT [`"/haowork-higress-binding-publisher`"]`n", [Text.UTF8Encoding]::new($false))

Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-core-bridge:local', $coreContext)
Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-mcp:local', $mcpContext)
Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-agentteams-binding-controller:local', $bindingControllerContext)
Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-higress-binding-publisher:local', $bindingPublisherContext)
$probeDockerfile = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\Dockerfile.network-probe'
Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-network-probe:local', '-f', $probeDockerfile, $imageRoot)
Invoke-P005V122CoreNative -FilePath $kind -Arguments @('load', 'docker-image', '--name', $ClusterName, 'haowork-core-bridge:local', 'haowork-mcp:local', 'haowork-agentteams-binding-controller:local', 'haowork-higress-binding-publisher:local', 'haowork-network-probe:local')

$bridgeToken = New-P005V122RandomHex
$model = [string](Get-Item -Path 'Env:HAOWORK_P005_PUBLIC_LLM_MODEL' -ErrorAction SilentlyContinue).Value
if ([string]::IsNullOrWhiteSpace($model) -or $model.Contains("`n") -or $model.Contains("`r")) { throw 'BLOCKED_RUNTIME_MODEL' }
$managerPod = & $kubectl get pod haowork-public-agentteams-manager -n haowork-public -o json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_MANAGER_RUNTIME' }
$managerImages = @($managerPod.spec.containers | Where-Object { [string]$_.name -ceq 'worker' -and [string]$_.image -ceq $lockedManagerImage } | ForEach-Object { [string]$_.image })
if ($managerImages.Count -ne 1) { throw 'BLOCKED_MANAGER_IMAGE' }
$managerImage = $managerImages[0]
$managerWorker = @($managerPod.spec.containers | Where-Object { [string]$_.name -ceq 'worker' })
if ($managerWorker.Count -ne 1) { throw 'BLOCKED_MANAGER_WORKER_CONTAINER' }
$managerBuckets = @($managerWorker[0].env | Where-Object { $_.name -eq 'AGENTTEAMS_FS_BUCKET' } | ForEach-Object { [string]$_.value })
if ($managerBuckets.Count -ne 1 -or $managerBuckets[0] -notmatch '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$') { throw 'BLOCKED_MANAGER_ARTIFACT_BUCKET' }
$managerBucket = $managerBuckets[0]
$appServiceTokenEncoded = ([string](& $kubectl get secret haowork-public-agentteams-runtime-env -n haowork-public -o 'jsonpath={.data.AGENTTEAMS_MATRIX_APPSERVICE_AS_TOKEN}')).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($appServiceTokenEncoded)) { throw 'BLOCKED_MATRIX_APPSERVICE_TOKEN' }
$appServiceToken = ConvertFrom-P005V122AppServiceToken -EncodedValue $appServiceTokenEncoded
Protect-P005V122SecretDirectory -Path $runtimeRoot
$runtimeEnvironmentPath = Join-Path $runtimeRoot 'core-bridge.env'
$runtimeEnvironmentHandle = $null
try {
$runtimeEnvironmentHandle = Write-P005V122SecretFile -Path $runtimeEnvironmentPath -Lines @("token=$bridgeToken", "model=$model", "manager-image=$managerImage", "bucket=$managerBucket", "matrix-appservice-as-token=$appServiceToken")
    $runtimeSecret = & $kubectl create secret generic haowork-core-bridge-runtime -n haowork-public --from-env-file=$runtimeEnvironmentPath --dry-run=client -o yaml
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CORE_BRIDGE_SECRET' }
    $runtimeSecret | & $kubectl apply -f - | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CORE_BRIDGE_SECRET' }
} finally {
    if ($null -ne $runtimeEnvironmentHandle) { $runtimeEnvironmentHandle.Dispose() }
    if (Test-Path -LiteralPath $runtimeEnvironmentPath) {
        try { Remove-Item -LiteralPath $runtimeEnvironmentPath -Force -ErrorAction Stop } catch { throw 'BLOCKED_CORE_BRIDGE_SECRET_CLEANUP' }
    }
    $appServiceToken = $null
    $appServiceTokenEncoded = $null
}

$internalBridgeToken = New-P005V122RandomHex
$internalModel = [string](Get-Item -Path 'Env:HAOWORK_P005_INTERNAL_LLM_MODEL' -ErrorAction SilentlyContinue).Value
if ([string]::IsNullOrWhiteSpace($internalModel) -or $internalModel.Contains("`n") -or $internalModel.Contains("`r")) { throw 'BLOCKED_RUNTIME_MODEL_INTERNAL' }
$internalManagerPod = & $kubectl get pod haowork-internal-agentteams-manager -n haowork-internal -o json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_MANAGER_RUNTIME_INTERNAL' }
$internalManagerImages = @($internalManagerPod.spec.containers | Where-Object { [string]$_.name -ceq 'worker' -and [string]$_.image -ceq $lockedManagerImage } | ForEach-Object { [string]$_.image })
if ($internalManagerImages.Count -ne 1) { throw 'BLOCKED_MANAGER_IMAGE_INTERNAL' }
$internalManagerWorker = @($internalManagerPod.spec.containers | Where-Object { [string]$_.name -ceq 'worker' })
if ($internalManagerWorker.Count -ne 1) { throw 'BLOCKED_MANAGER_WORKER_CONTAINER_INTERNAL' }
$internalManagerBuckets = @($internalManagerWorker[0].env | Where-Object { $_.name -eq 'AGENTTEAMS_FS_BUCKET' } | ForEach-Object { [string]$_.value })
if ($internalManagerBuckets.Count -ne 1 -or $internalManagerBuckets[0] -notmatch '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$') { throw 'BLOCKED_MANAGER_ARTIFACT_BUCKET_INTERNAL' }
$internalAppServiceTokenEncoded = ([string](& $kubectl get secret haowork-internal-agentteams-runtime-env -n haowork-internal -o 'jsonpath={.data.AGENTTEAMS_MATRIX_APPSERVICE_AS_TOKEN}')).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($internalAppServiceTokenEncoded)) { throw 'BLOCKED_MATRIX_APPSERVICE_TOKEN_INTERNAL' }
$internalAppServiceToken = ConvertFrom-P005V122AppServiceToken -EncodedValue $internalAppServiceTokenEncoded
$internalEnvironmentPath = Join-Path $runtimeRoot 'core-bridge-internal.env'
$internalEnvironmentHandle = $null
try {
    $internalEnvironmentHandle = Write-P005V122SecretFile -Path $internalEnvironmentPath -Lines @("token=$internalBridgeToken", "model=$internalModel", "manager-image=$($internalManagerImages[0])", "bucket=$($internalManagerBuckets[0])", "matrix-appservice-as-token=$internalAppServiceToken")
    $internalRuntimeSecret = & $kubectl create secret generic haowork-core-bridge-runtime -n haowork-internal --from-env-file=$internalEnvironmentPath --dry-run=client -o yaml
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CORE_BRIDGE_SECRET_INTERNAL' }
    $internalRuntimeSecret | & $kubectl apply -f - | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CORE_BRIDGE_SECRET_INTERNAL' }
} finally {
    if ($null -ne $internalEnvironmentHandle) { $internalEnvironmentHandle.Dispose() }
    if (Test-Path -LiteralPath $internalEnvironmentPath) {
        try { Remove-Item -LiteralPath $internalEnvironmentPath -Force -ErrorAction Stop } catch { throw 'BLOCKED_CORE_BRIDGE_SECRET_CLEANUP_INTERNAL' }
    }
    $internalAppServiceToken = $null
    $internalAppServiceTokenEncoded = $null
}

Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-n', 'haowork-public', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-core-bridge.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-n', 'haowork-public', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-mcp.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-n', 'haowork-public', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-binding-controller.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-higress-binding-publisher.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-core-bridge-internal.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-mcp-internal.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-binding-controller-internal.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-higress-binding-publisher-internal.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-network-probe-public.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-network-probe-internal.yaml'))
foreach ($namespace in @('haowork-public', 'haowork-internal')) {
    foreach ($deployment in @(
        'haowork-core-bridge',
        'haowork-mcp',
        'haowork-agentteams-binding-controller',
        'haowork-higress-binding-publisher',
        'haowork-network-probe'
    )) {
        Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'restart', "deployment/$deployment", '-n', $namespace)
        Wait-P005V122LocalDeploymentObserved -Kubectl $kubectl -Namespace $namespace -Deployment $deployment
    }
}
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-core-bridge', '-n', 'haowork-public', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-core-bridge', '-n', 'haowork-internal', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-agentteams-binding-controller', '-n', 'haowork-public', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-agentteams-binding-controller', '-n', 'haowork-internal', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-network-probe', '-n', 'haowork-public', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-network-probe', '-n', 'haowork-internal', '--timeout=180s')

foreach ($zone in @(
    [pscustomobject]@{ Name = 'public'; Namespace = 'haowork-public'; Port = 18083 },
    [pscustomobject]@{ Name = 'internal'; Namespace = 'haowork-internal'; Port = 18084 }
)) {
    $consoleSecret = & $kubectl get secret higress-console -n $zone.Namespace -o json
    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_HIGRESS_CONSOLE_SECRET_$($zone.Name.ToUpperInvariant())" }
    $consoleData = ($consoleSecret | ConvertFrom-Json).data
    $consoleUsername = ConvertFrom-P005V122Base64 -Value ([string]$consoleData.adminUsername)
    $consolePassword = ConvertFrom-P005V122Base64 -Value ([string]$consoleData.adminPassword)
    $portForwardOut = Join-Path $runtimeRoot "higress-$($zone.Name)-port-forward.out.log"
    $portForwardErr = Join-Path $runtimeRoot "higress-$($zone.Name)-port-forward.err.log"
    $portForward = Start-Process -FilePath $kubectl -ArgumentList @('port-forward', '-n', $zone.Namespace, 'service/higress-console', "$($zone.Port):8080") -PassThru -WindowStyle Hidden -RedirectStandardOutput $portForwardOut -RedirectStandardError $portForwardErr
    try {
        $ready = $false
        for ($attempt = 0; $attempt -lt 40; $attempt++) {
            try {
                $client = New-Object Net.Sockets.TcpClient
                $client.Connect('127.0.0.1', $zone.Port)
                $client.Dispose()
                $ready = $true
                break
            } catch { Start-Sleep -Milliseconds 250 }
        }
        if (-not $ready) { throw "BLOCKED_HIGRESS_CONSOLE_PORT_FORWARD_$($zone.Name.ToUpperInvariant())" }
        $baseURL = "http://127.0.0.1:$($zone.Port)"
        $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
        Invoke-P005V122HigressJSON -BaseURL $baseURL -Method POST -Path '/session/login' -Body @{ username = $consoleUsername; password = $consolePassword } -Session $session | Out-Null
        $sources = Invoke-P005V122HigressJSON -BaseURL $baseURL -Method GET -Path '/v1/service-sources' -Body $null -Session $session
        $sourceExists = @($sources.data | Where-Object { $_.name -eq 'haowork-mcp-backend' }).Count -eq 1
        if (-not $sourceExists) {
            Invoke-P005V122HigressJSON -BaseURL $baseURL -Method POST -Path '/v1/service-sources' -Body @{ type = 'dns'; name = 'haowork-mcp-backend'; domain = "haowork-mcp.$($zone.Namespace).svc.cluster.local"; port = 8080; protocol = 'http' } -Session $session | Out-Null
        }
        $mcpBody = @{
            name = 'haowork-mcp'; mcpServerName = 'haowork-mcp'; description = 'Haowork governed skills'; type = 'DIRECT_ROUTE'
            domains = @('aigw-local.agentteams.io')
            services = @(@{ name = 'haowork-mcp-backend.dns'; port = 8080; weight = 100 })
            consumerAuthInfo = @{ type = 'key-auth'; enable = $true; allowedConsumers = @() }
            directRouteConfig = @{ path = '/mcp'; transportType = 'streamable' }
        }
        Invoke-P005V122HigressJSON -BaseURL $baseURL -Method PUT -Path '/v1/mcpServer' -Body $mcpBody -Session $session | Out-Null
    } finally {
        if ($null -ne $portForward -and -not $portForward.HasExited) { Stop-Process -Id $portForward.Id -Force }
        $consolePassword = $null
    }
}

foreach ($zone in @('haowork-public', 'haowork-internal')) {
    Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-higress-binding-publisher', '-n', $zone, '--timeout=180s')
}
Write-Host 'P0-05 Core Bridge and structured receipt controllers ready; MCP becomes ready after Delivery Leader binding publication'
