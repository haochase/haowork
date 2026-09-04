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

function Write-P005V122SecretFile {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string[]]$Lines
    )

    $stream = $null
    try {
        $security = New-Object System.Security.AccessControl.FileSecurity
        $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $security.SetOwner($currentSid)
        $security.SetAccessRuleProtection($true, $false)
        foreach ($sid in @($currentSid, (New-Object Security.Principal.SecurityIdentifier('S-1-5-18')), (New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))) {
            [void]$security.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::FullControl, [Security.AccessControl.AccessControlType]::Allow)))
        }
        if (Test-Path -LiteralPath $Path) { Remove-Item -LiteralPath $Path -Force -ErrorAction Stop }
        $stream = [IO.FileStream]::new($Path, [IO.FileMode]::CreateNew, [Security.AccessControl.FileSystemRights]::FullControl, [IO.FileShare]::Read, 4096, [IO.FileOptions]::None, $security)
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

    try {
        $security = New-Object System.Security.AccessControl.DirectorySecurity
        $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $security.SetOwner($currentSid)
        $security.SetAccessRuleProtection($true, $false)
        $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
        foreach ($sid in @($currentSid, (New-Object Security.Principal.SecurityIdentifier('S-1-5-18')), (New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))) {
            [void]$security.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::FullControl, $inheritance, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)))
        }
        Set-Acl -LiteralPath $Path -AclObject $security -ErrorAction Stop
    } catch {
        throw 'BLOCKED_CORE_BRIDGE_SECRET_ACL'
    }
}

function Invoke-P005V122HigressJSON {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Path,
        [AllowNull()][object]$Body,
        [Parameter(Mandatory)][Microsoft.PowerShell.Commands.WebRequestSession]$Session
    )

    $parameters = @{ Method = $Method; Uri = "http://127.0.0.1:18083$Path"; WebSession = $Session; UseBasicParsing = $true }
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
    Invoke-P005V122CoreNative -FilePath $go -Arguments @('build', '-trimpath', '-ldflags=-s -w', '-o', (Join-Path $imageRoot 'haowork-network-probe'), './cmd/haowork-network-probe')
} finally {
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $originalGOOS, $originalGOARCH, $originalCGO
}

$coreContext = Join-Path $tempRoot 'core-image'
$mcpContext = Join-Path $tempRoot 'mcp-image'
$safeTempRoot = [IO.Path]::GetFullPath($tempRoot).TrimEnd('\') + '\'
foreach ($path in @($coreContext, $mcpContext)) {
    if (-not [IO.Path]::GetFullPath($path).StartsWith($safeTempRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'BLOCKED_CORE_BRIDGE_BUILD_CONTEXT'
    }
}
Remove-Item -LiteralPath $coreContext, $mcpContext -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $coreContext, (Join-Path $mcpContext 'skills') | Out-Null
Copy-Item -LiteralPath (Join-Path $imageRoot 'haowork-core-bridge') -Destination (Join-Path $coreContext 'haowork-core-bridge')
Copy-Item -LiteralPath (Join-Path $imageRoot 'haowork-mcp') -Destination (Join-Path $mcpContext 'haowork-mcp')
Copy-Item -Path (Join-Path $worktreeRoot 'skills\*') -Destination (Join-Path $mcpContext 'skills') -Recurse
[IO.File]::WriteAllText((Join-Path $coreContext 'Dockerfile'), "FROM scratch`nCOPY haowork-core-bridge /haowork-core-bridge`nUSER 65532:65532`nENTRYPOINT [`"/haowork-core-bridge`"]`n", [Text.UTF8Encoding]::new($false))
[IO.File]::WriteAllText((Join-Path $mcpContext 'Dockerfile'), "FROM scratch`nCOPY haowork-mcp /haowork-mcp`nCOPY skills /opt/haowork/skills`nUSER 65532:65532`nENTRYPOINT [`"/haowork-mcp`"]`n", [Text.UTF8Encoding]::new($false))

Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-core-bridge:local', $coreContext)
Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-mcp:local', $mcpContext)
$probeDockerfile = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\Dockerfile.network-probe'
Invoke-P005V122CoreNative -FilePath $docker -Arguments @('build', '--pull=false', '-t', 'haowork-network-probe:local', '-f', $probeDockerfile, $imageRoot)
Invoke-P005V122CoreNative -FilePath $kind -Arguments @('load', 'docker-image', '--name', $ClusterName, 'haowork-core-bridge:local', 'haowork-mcp:local', 'haowork-network-probe:local')

$bridgeToken = New-P005V122RandomHex
$model = [string](Get-Item -Path 'Env:HAOWORK_P005_PUBLIC_LLM_MODEL' -ErrorAction SilentlyContinue).Value
if ([string]::IsNullOrWhiteSpace($model) -or $model.Contains("`n") -or $model.Contains("`r")) { throw 'BLOCKED_RUNTIME_MODEL' }
$managerPod = & $kubectl get pod haowork-public-agentteams-manager -n haowork-public -o json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_MANAGER_MATRIX_TOKEN' }
$managerImages = @($managerPod.spec.containers | Where-Object { [string]$_.name -ceq 'worker' -and [string]$_.image -ceq $lockedManagerImage } | ForEach-Object { [string]$_.image })
if ($managerImages.Count -ne 1) { throw 'BLOCKED_MANAGER_IMAGE' }
$managerImage = $managerImages[0]
$managerMatrixTokens = @($managerPod.spec.containers[0].env | Where-Object { $_.name -eq 'AGENTTEAMS_MANAGER_MATRIX_TOKEN' } | ForEach-Object { [string]$_.value })
$managerBuckets = @($managerPod.spec.containers[0].env | Where-Object { $_.name -eq 'AGENTTEAMS_FS_BUCKET' } | ForEach-Object { [string]$_.value })
if ($managerMatrixTokens.Count -ne 1 -or [string]::IsNullOrWhiteSpace($managerMatrixTokens[0]) -or $managerMatrixTokens[0].Contains("`n") -or $managerMatrixTokens[0].Contains("`r")) {
    throw 'BLOCKED_MANAGER_MATRIX_TOKEN'
}
if ($managerBuckets.Count -ne 1 -or $managerBuckets[0] -notmatch '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$') { throw 'BLOCKED_MANAGER_ARTIFACT_BUCKET' }
$managerMatrixToken = $managerMatrixTokens[0]
$managerBucket = $managerBuckets[0]
Protect-P005V122SecretDirectory -Path $runtimeRoot
$runtimeEnvironmentPath = Join-Path $runtimeRoot 'core-bridge.env'
$runtimeEnvironmentHandle = $null
try {
$runtimeEnvironmentHandle = Write-P005V122SecretFile -Path $runtimeEnvironmentPath -Lines @("token=$bridgeToken", "model=$model", "manager-image=$managerImage", "matrix-token=$managerMatrixToken", "bucket=$managerBucket")
    $runtimeSecret = & $kubectl create secret generic haowork-core-bridge-runtime -n haowork-public --from-env-file=$runtimeEnvironmentPath --dry-run=client -o yaml
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CORE_BRIDGE_SECRET' }
    $runtimeSecret | & $kubectl apply -f - | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_CORE_BRIDGE_SECRET' }
} finally {
    if ($null -ne $runtimeEnvironmentHandle) { $runtimeEnvironmentHandle.Dispose() }
    if (Test-Path -LiteralPath $runtimeEnvironmentPath) {
        try { Remove-Item -LiteralPath $runtimeEnvironmentPath -Force -ErrorAction Stop } catch { throw 'BLOCKED_CORE_BRIDGE_SECRET_CLEANUP' }
    }
}

$gatewayKeyEncoded = (& $kubectl get secret agentteams-creds-default -n haowork-public -o 'jsonpath={.data.WORKER_GATEWAY_KEY}').Trim()
$managerPrincipal = (& $kubectl get manager default -n haowork-public -o 'jsonpath={.status.matrixUserID}').Trim()
if ([string]::IsNullOrWhiteSpace($gatewayKeyEncoded) -or [string]::IsNullOrWhiteSpace($managerPrincipal)) { throw 'BLOCKED_MCP_RUNTIME_BINDING' }
$gatewayKey = ConvertFrom-P005V122Base64 -Value $gatewayKeyEncoded
$sha = [Security.Cryptography.SHA256]::Create()
try { $credentialDigest = ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($gatewayKey)))).Replace('-', '').ToLowerInvariant() } finally { $sha.Dispose() }
$bindingsPath = Join-Path $runtimeRoot 'bindings.json'
$bindingDocument = @{ bindings = @(@{
    consumer_name = 'manager'; credential_sha256 = $credentialDigest
    principal = @{ logical_actor_id = 'AGT-P005-MANAGER'; runtime_principal_id = $managerPrincipal; environment_id = 'public'; agentteams_instance_id = 'default'; binding_revision = 1 }
}) }
[IO.File]::WriteAllText($bindingsPath, ($bindingDocument | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
$bindingSecret = & $kubectl create secret generic haowork-mcp-runtime-bindings -n haowork-public --from-file=bindings.json=$bindingsPath --dry-run=client -o yaml
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_MCP_RUNTIME_BINDING' }
$bindingSecret | & $kubectl apply -f - | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_MCP_RUNTIME_BINDING' }

Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-n', 'haowork-public', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-core-bridge.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-n', 'haowork-public', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-mcp.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-network-probe-public.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('apply', '-f', (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\haowork-network-probe-internal.yaml'))
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'restart', 'deployment/haowork-core-bridge', '-n', 'haowork-public')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'restart', 'deployment/haowork-mcp', '-n', 'haowork-public')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-core-bridge', '-n', 'haowork-public', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-mcp', '-n', 'haowork-public', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-network-probe', '-n', 'haowork-public', '--timeout=180s')
Invoke-P005V122CoreNative -FilePath $kubectl -Arguments @('rollout', 'status', 'deployment/haowork-network-probe', '-n', 'haowork-internal', '--timeout=180s')

$consoleSecret = & $kubectl get secret higress-console -n haowork-public -o json
if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_HIGRESS_CONSOLE_SECRET' }
$consoleData = ($consoleSecret | ConvertFrom-Json).data
$consoleUsername = ConvertFrom-P005V122Base64 -Value ([string]$consoleData.adminUsername)
$consolePassword = ConvertFrom-P005V122Base64 -Value ([string]$consoleData.adminPassword)
$portForwardOut = Join-Path $runtimeRoot 'higress-port-forward.out.log'
$portForwardErr = Join-Path $runtimeRoot 'higress-port-forward.err.log'
$portForward = Start-Process -FilePath $kubectl -ArgumentList @('port-forward', '-n', 'haowork-public', 'service/higress-console', '18083:8080') -PassThru -WindowStyle Hidden -RedirectStandardOutput $portForwardOut -RedirectStandardError $portForwardErr
try {
    $ready = $false
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        try {
            $client = New-Object Net.Sockets.TcpClient
            $client.Connect('127.0.0.1', 18083)
            $client.Dispose()
            $ready = $true
            break
        } catch { Start-Sleep -Milliseconds 250 }
    }
    if (-not $ready) { throw 'BLOCKED_HIGRESS_CONSOLE_PORT_FORWARD' }
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    Invoke-P005V122HigressJSON -Method POST -Path '/session/login' -Body @{ username = $consoleUsername; password = $consolePassword } -Session $session | Out-Null
    $sources = Invoke-P005V122HigressJSON -Method GET -Path '/v1/service-sources' -Body $null -Session $session
    $sourceExists = @($sources.data | Where-Object { $_.name -eq 'haowork-mcp-backend' }).Count -eq 1
    if (-not $sourceExists) {
        Invoke-P005V122HigressJSON -Method POST -Path '/v1/service-sources' -Body @{ type = 'dns'; name = 'haowork-mcp-backend'; domain = 'haowork-mcp.haowork-public.svc.cluster.local'; port = 8080; protocol = 'http' } -Session $session | Out-Null
    }
    $mcpBody = @{
        name = 'haowork-mcp'; mcpServerName = 'haowork-mcp'; description = 'Haowork governed skills'; type = 'DIRECT_ROUTE'
        domains = @('aigw-local.agentteams.io')
        services = @(@{ name = 'haowork-mcp-backend.dns'; port = 8080; weight = 100 })
        consumerAuthInfo = @{ type = 'key-auth'; enable = $true; allowedConsumers = @('manager') }
        directRouteConfig = @{ path = '/mcp'; transportType = 'streamable' }
    }
    Invoke-P005V122HigressJSON -Method PUT -Path '/v1/mcpServer' -Body $mcpBody -Session $session | Out-Null
    Invoke-P005V122HigressJSON -Method PUT -Path '/v1/mcpServer/consumers' -Body @{ mcpServerName = 'haowork-mcp'; consumers = @('manager') } -Session $session | Out-Null
} finally {
    if ($null -ne $portForward -and -not $portForward.HasExited) { Stop-Process -Id $portForward.Id -Force }
}

$coreReady = (& $kubectl get deployment haowork-core-bridge -n haowork-public -o 'jsonpath={.status.readyReplicas}').Trim()
$mcpReady = (& $kubectl get deployment haowork-mcp -n haowork-public -o 'jsonpath={.status.readyReplicas}').Trim()
if ($coreReady -ne '1' -or $mcpReady -ne '1') { throw 'BLOCKED_HAOWORK_CORE_BRIDGE' }
Write-Host 'P0-05 Core Bridge and governed MCP deployment ready'
