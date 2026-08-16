[CmdletBinding()]
param(
    [ValidateSet('RenderOnly', 'ClusterOnly', 'LocalDualNamespace')]
    [string]$Mode = 'LocalDualNamespace',
    [switch]$Json,
    [switch]$RequireBrowserEndpoint,
    [string]$ClusterName = 'haowork-p005-v122',
    [switch]$SkipDeploymentLock
)

$ErrorActionPreference = 'Stop'

function Get-P005V122WorktreeRoot {
    return Split-Path -Parent $PSScriptRoot
}

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
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($commonGitDir)) {
        throw "BLOCKED_GIT_WORKTREE: cannot resolve the shared git directory from $WorktreeRoot"
    }
    return Split-Path -Parent ([IO.Path]::GetFullPath($commonGitDir))
}

function Initialize-P005V122Cache {
    param([Parameter(Mandatory)][string]$RepoRoot)

    $cacheRoot = Join-Path $RepoRoot '.haowork\cache'
    foreach ($name in @('tmp', 'go', 'gomod', 'npm', 'helm', 'kind', 'evidence\p0-05-v1.2.2', 'helm\config', 'helm\data')) {
        New-Item -ItemType Directory -Force (Join-Path $cacheRoot $name) | Out-Null
    }
    $env:TEMP = Join-Path $cacheRoot 'tmp'
    $env:TMP = $env:TEMP
    $env:GOCACHE = Join-Path $cacheRoot 'go'
    $env:GOMODCACHE = Join-Path $cacheRoot 'gomod'
    $env:npm_config_cache = Join-Path $cacheRoot 'npm'
    $env:HELM_CACHE_HOME = Join-Path $cacheRoot 'helm'
    $env:HELM_CONFIG_HOME = Join-Path $cacheRoot 'helm\config'
    $env:HELM_DATA_HOME = Join-Path $cacheRoot 'helm\data'
    $env:KIND_EXPERIMENTAL_DOCKER_NETWORK = 'kind'
    return $cacheRoot
}

function Resolve-P005V122Executable {
    param(
        [Parameter(Mandatory)][string]$WorktreeRoot,
        [Parameter(Mandatory)][string]$Name
    )

    $repoRoot = Get-P005V122RepoRoot -WorktreeRoot $WorktreeRoot
    $local = Join-Path $repoRoot ".tools\bin\$Name.exe"
    if (Test-Path -LiteralPath $local -PathType Leaf) {
        return $local
    }
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -ne $command) {
        return $command.Source
    }
    return $null
}

function Get-P005V122ContractState {
    param([Parameter(Mandatory)][string]$WorktreeRoot)

    $lockPath = Join-Path $WorktreeRoot 'deploy\agentteams\v1.2.2\upstream.lock.json'
    $commonPath = Join-Path $WorktreeRoot 'scripts\p0-05-v122-common.ps1'
    if (-not (Test-Path -LiteralPath $lockPath -PathType Leaf) -or -not (Test-Path -LiteralPath $commonPath -PathType Leaf)) {
        return @{ State = 'BLOCKED_UPSTREAM_CONTRACT'; Contract = $null; LockPath = $lockPath }
    }
    . $commonPath
    try {
        $contract = Get-P005V122OfficialContract -ContractPath $lockPath
        Assert-P005V122OfficialContract -Contract $contract
        $valuesReady = $true
        foreach ($valuesPath in @(
            (Join-Path $WorktreeRoot 'deploy\agentteams\v1.2.2\public-values.yaml'),
            (Join-Path $WorktreeRoot 'deploy\agentteams\v1.2.2\internal-values.yaml')
        )) {
            if (-not (Test-P005V122ValuesDeploymentImagesReady -Contract $contract -ValuesPath $valuesPath)) {
                $valuesReady = $false
                break
            }
        }
        return @{ State = 'READY'; Contract = $contract; LockPath = $lockPath; DeploymentImagesReady = $valuesReady }
    } catch {
        return @{ State = 'BLOCKED_UPSTREAM_CONTRACT'; Contract = $null; LockPath = $lockPath; Detail = $_.Exception.Message }
    }
}

function Get-P005V122SourceState {
    param(
        [Parameter(Mandatory)][string]$RepoRoot,
        [Parameter(Mandatory)][hashtable]$Contract
    )

    $upstreamRoot = Join-Path $RepoRoot '.haowork\cache\upstream\AgentTeams-v1.2.2'
    try {
        # The contract loader runs in a function scope, so load the common
        # validation API again in this scope before calling it.
        . (Join-Path $PSScriptRoot 'p0-05-v122-common.ps1')
        $sourceIsValid = Test-P005V122OfficialSource -Contract $Contract -UpstreamRoot $upstreamRoot
        if (-not [bool]$sourceIsValid) {
            return @{ State = 'BLOCKED_UPSTREAM_SOURCE'; UpstreamRoot = $upstreamRoot }
        }
        return @{ State = 'READY'; UpstreamRoot = $upstreamRoot }
    } catch {
        return @{ State = 'BLOCKED_UPSTREAM_SOURCE'; UpstreamRoot = $upstreamRoot; Detail = $_.Exception.Message }
    }
}

function Get-P005V122MemoryGiB {
    if ($env:NUMBER_OF_PROCESSORS) {
        try {
            $memoryBytes = (Get-CimInstance Win32_ComputerSystem -ErrorAction Stop).TotalPhysicalMemory
            return [math]::Round($memoryBytes / 1GB, 1)
        } catch {
            return 0
        }
    }
    return 0
}

function Get-P005V122DockerStorageEvidence {
    $settingsPath = Join-Path $env:APPDATA 'Docker\settings-store.json'
    if (-not (Test-Path -LiteralPath $settingsPath -PathType Leaf)) {
        return [ordered]@{ status = 'BLOCKED_DOCKER_STORAGE'; configured_directory = $null; vhdx_path = $null; vhdx_exists = $false }
    }

    try {
        $settings = Get-Content -LiteralPath $settingsPath -Raw -Encoding utf8 | ConvertFrom-Json
        $configuredDirectory = [string]$settings.CustomWslDistroDir
    } catch {
        return [ordered]@{ status = 'BLOCKED_DOCKER_STORAGE'; configured_directory = $null; vhdx_path = $null; vhdx_exists = $false }
    }
    if ([string]::IsNullOrWhiteSpace($configuredDirectory)) {
        return [ordered]@{ status = 'BLOCKED_DOCKER_STORAGE'; configured_directory = $null; vhdx_path = $null; vhdx_exists = $false }
    }

    $configuredDirectory = [IO.Path]::GetFullPath($configuredDirectory)
    $vhdxPath = Join-Path $configuredDirectory 'main\ext4.vhdx'
    $onE = $configuredDirectory.StartsWith('E:\', [StringComparison]::OrdinalIgnoreCase)
    $vhdxExists = Test-Path -LiteralPath $vhdxPath -PathType Leaf
    return [ordered]@{
        status = if ($onE -and $vhdxExists) { 'READY' } else { 'BLOCKED_DOCKER_STORAGE' }
        configured_directory = $configuredDirectory
        vhdx_path = $vhdxPath
        vhdx_exists = $vhdxExists
    }
}

function Test-P005V122DockerDaemon {
    param([string]$Docker)

    if ([string]::IsNullOrWhiteSpace($Docker)) { return $false }
    & $Docker version --format '{{.Server.Version}}' 2>$null | Out-Null
    return $LASTEXITCODE -eq 0
}

function Get-P005V122DockerCgroupVersion {
    param([string]$Docker)

    if ([string]::IsNullOrWhiteSpace($Docker)) { return '' }
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $value = & $Docker info --format '{{.CgroupVersion}}' 2>$null
        if ($LASTEXITCODE -ne 0) { return '' }
        return ([string]($value -join '')).Trim()
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
}

function Test-P005V122CIDR {
    param([Parameter(Mandatory)][string]$Value)

    $parts = $Value -split '/', 2
    if ($parts.Count -ne 2 -or [string]::IsNullOrWhiteSpace($parts[0]) -or $parts[1] -notmatch '^(0|[1-9]\d*)$') { return $false }
    $ip = $null
    if (-not [System.Net.IPAddress]::TryParse($parts[0], [ref]$ip) -or $ip.ToString() -cne $parts[0]) { return $false }
    $prefixLength = [int]$parts[1]
    $bytes = $ip.GetAddressBytes()
    $maximumPrefixLength = $bytes.Length * 8
    if ($prefixLength -le 0 -or $prefixLength -gt $maximumPrefixLength -or
        $ip.Equals([System.Net.IPAddress]::IPv4Any) -or $ip.Equals([System.Net.IPAddress]::IPv6Any)) { return $false }
    if (($bytes.Length -eq 4 -and $bytes[0] -ge 224 -and $bytes[0] -le 239) -or
        ($bytes.Length -eq 16 -and $bytes[0] -eq 255)) { return $false }

    $fullBytes = [math]::Floor($prefixLength / 8)
    $remainingBits = $prefixLength % 8
    if ($remainingBits -gt 0) {
        $mask = (0xff -shl (8 - $remainingBits)) -band 0xff
        $hostMask = (-bnot $mask) -band 0xff
        if (($bytes[$fullBytes] -band $hostMask) -ne 0) { return $false }
        $fullBytes++
    }
    for ($index = $fullBytes; $index -lt $bytes.Length; $index++) {
        if ($bytes[$index] -ne 0) { return $false }
    }
    return $true
}

function Test-P005V122ExternalEgressCIDRs {
    param([Parameter(Mandatory)][string]$Zone)

    $raw = [string](Get-Item -Path "Env:HAOWORK_P005_$($Zone.ToUpperInvariant())_EGRESS_CIDRS" -ErrorAction SilentlyContinue).Value
    $cidrs = @($raw -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
    return $cidrs.Count -gt 0 -and @($cidrs | Where-Object { -not (Test-P005V122CIDR -Value $_) }).Count -eq 0
}

function Test-P005V122IPAddressInCIDR {
    param(
        [Parameter(Mandatory)][System.Net.IPAddress]$IPAddress,
        [Parameter(Mandatory)][string]$CIDR
    )

    $parts = $CIDR -split '/', 2
    $network = $null
    if ($parts.Count -ne 2 -or -not [System.Net.IPAddress]::TryParse($parts[0], [ref]$network)) { return $false }
    $addressBytes = $IPAddress.GetAddressBytes()
    $networkBytes = $network.GetAddressBytes()
    if ($addressBytes.Length -ne $networkBytes.Length) { return $false }
    $prefixLength = [int]$parts[1]
    $fullBytes = [math]::Floor($prefixLength / 8)
    $remainingBits = $prefixLength % 8
    for ($index = 0; $index -lt $fullBytes; $index++) {
        if ($addressBytes[$index] -ne $networkBytes[$index]) { return $false }
    }
    if ($remainingBits -eq 0) { return $true }
    $mask = (0xff -shl (8 - $remainingBits)) -band 0xff
    return ($addressBytes[$fullBytes] -band $mask) -eq ($networkBytes[$fullBytes] -band $mask)
}

function Test-P005V122RuntimeProviderEndpoint {
    param(
        [Parameter(Mandatory)][string]$Zone,
        [int]$TimeoutMilliseconds = 8000
    )

    $prefix = "HAOWORK_P005_$($Zone.ToUpperInvariant())"
    $apiKey = [string](Get-Item -Path "Env:${prefix}_LLM_API_KEY" -ErrorAction SilentlyContinue).Value
    $baseUrl = [string](Get-Item -Path "Env:${prefix}_LLM_BASE_URL" -ErrorAction SilentlyContinue).Value
    $rawCIDRs = [string](Get-Item -Path "Env:${prefix}_EGRESS_CIDRS" -ErrorAction SilentlyContinue).Value
    try {
        $uri = [uri]$baseUrl
        if ($uri.Scheme -notin @('http', 'https') -or [string]::IsNullOrWhiteSpace($uri.Host) -or $uri.Port -le 0 -or [string]::IsNullOrWhiteSpace($apiKey)) { return $false }
        $cidrs = @($rawCIDRs -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
        if ($cidrs.Count -eq 0) { return $false }
        $addresses = @([System.Net.Dns]::GetHostAddresses($uri.Host))
        if ($addresses.Count -eq 0) { return $false }
        foreach ($address in $addresses) {
            $covered = $false
            foreach ($cidr in $cidrs) {
                if (Test-P005V122IPAddressInCIDR -IPAddress $address -CIDR $cidr) {
                    $covered = $true
                    break
                }
            }
            if (-not $covered) { return $false }
        }
        $builder = New-Object System.UriBuilder($uri)
        $builder.Query = ''
        $basePath = $builder.Path.TrimEnd('/')
        $builder.Path = if ($basePath -match '/models$') { $basePath } elseif ([string]::IsNullOrWhiteSpace($basePath) -or $basePath -eq '/') { '/models' } else { "$basePath/models" }
        for ($attempt = 1; $attempt -le 3; $attempt++) {
            $response = $null
            $statusCode = 0
            try {
                $request = [System.Net.HttpWebRequest]::Create($builder.Uri)
                $request.Method = 'GET'
                $request.Timeout = $TimeoutMilliseconds
                $request.ReadWriteTimeout = $TimeoutMilliseconds
                $request.Proxy = $null
                $request.Headers['Authorization'] = "Bearer $apiKey"
                try {
                    $response = $request.GetResponse()
                } catch [System.Net.WebException] {
                    $response = $_.Exception.Response
                }
                if ($null -ne $response) {
                    $statusCode = [int]$response.StatusCode
                    if ($statusCode -ge 200 -and $statusCode -lt 300) { return $true }
                    if ($statusCode -ne 408 -and $statusCode -ne 429 -and $statusCode -lt 500) { return $false }
                }
            } finally {
                if ($null -ne $response) { $response.Close() }
            }
            if ($attempt -lt 3) { Start-Sleep -Milliseconds 250 }
        }
        return $false
    } catch {
        return $false
    }
}

function Get-P005V122BrowserEndpoints {
    return @(
        [ordered]@{ zone = 'public'; url = 'http://127.0.0.1:18080'; port = 18080 },
        [ordered]@{ zone = 'internal'; url = 'http://127.0.0.1:18082'; port = 18082 }
    )
}

function Invoke-P005V122WithEvidenceLock {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [int]$TimeoutSeconds = 300
    )

    $mutex = New-Object System.Threading.Mutex($false, 'Haowork.P005.AgentTeams.V122.PreflightEvidence')
    $hasLock = $false
    try {
        try {
            $hasLock = $mutex.WaitOne([TimeSpan]::FromSeconds($TimeoutSeconds))
        } catch [System.Threading.AbandonedMutexException] {
            $hasLock = $true
        }
        if (-not $hasLock) { throw 'BLOCKED_PREFLIGHT_EVIDENCE_LOCK_TIMEOUT' }
        & $Action
    } finally {
        if ($hasLock) { $mutex.ReleaseMutex() }
        $mutex.Dispose()
    }
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

function Test-P005V122BrowserEndpoint {
    param(
        [Parameter(Mandatory)][string]$URL,
        [int]$TimeoutMilliseconds = 1000
    )

    try {
        $uri = [uri]$URL
        if ($uri.Scheme -ne 'http' -or $uri.Host -ne '127.0.0.1' -or $uri.Port -le 0) { return $false }
        $request = [System.Net.HttpWebRequest]::Create($uri)
        $request.Method = 'GET'
        $request.Timeout = $TimeoutMilliseconds
        $request.ReadWriteTimeout = $TimeoutMilliseconds
        $response = $null
        try {
            $response = $request.GetResponse()
        } catch [System.Net.WebException] {
            $response = $_.Exception.Response
        }
        if ($null -eq $response) { return $false }
        try {
            $statusCode = [int]$response.StatusCode
            return $statusCode -ge 200 -and $statusCode -lt 500
        } finally {
            $response.Close()
        }
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

function Test-P005V122ProcessOwnsBrowserPort {
    param(
        [Parameter(Mandatory)][int]$ProcessId,
        [Parameter(Mandatory)][int]$Port
    )

    try {
        $listeners = @(Get-NetTCPConnection -State Listen -LocalAddress '127.0.0.1' -LocalPort $Port -ErrorAction Stop)
        return @($listeners | Where-Object { $_.OwningProcess -eq $ProcessId }).Count -eq 1
    } catch {
        return $false
    }
}

function Test-P005V122ManagedBrowserEndpoint {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][string]$Zone,
        [Parameter(Mandatory)][string]$URL,
        [Parameter(Mandatory)][int]$Port
    )

    try {
        $entries = @(Read-P005V122BrowserPortForwardState -CacheRoot $CacheRoot -ClusterName $ClusterName |
            Where-Object { [string]$_.zone -eq $Zone })
        if ($entries.Count -ne 1) { return $false }
        $entry = $entries[0]
        $processId = [int]$entry.pid
        $processStartTimeUtc = [string]$entry.process_start_time_utc
        $kubeconfigPath = [string]$entry.kubeconfig_path
        $kubectlPath = [string]$entry.kubectl_path
        $cacheDirectory = [string]$entry.cache_directory
        $runToken = [string]$entry.run_token
        if ([string]::IsNullOrWhiteSpace($processStartTimeUtc) -or
            [string]::IsNullOrWhiteSpace($kubeconfigPath) -or
            [string]::IsNullOrWhiteSpace($kubectlPath) -or
            [string]::IsNullOrWhiteSpace($cacheDirectory) -or
            [string]::IsNullOrWhiteSpace($runToken)) { return $false }
        if (-not (Test-P005V122ManagedBrowserCacheDirectory -CacheRoot $CacheRoot -ClusterName $ClusterName -Zone $Zone -CacheDirectory $cacheDirectory -RunToken $runToken)) { return $false }
        if (-not (Test-P005V122ManagedBrowserPortForward -ProcessId $processId -Namespace ([string]$entry.namespace) -Port $Port -ProcessStartTimeUtc $processStartTimeUtc -KubeconfigPath $kubeconfigPath -KubectlPath $kubectlPath -CacheDirectory $cacheDirectory -RunToken $runToken)) { return $false }
        if (-not (Test-P005V122ProcessOwnsBrowserPort -ProcessId $processId -Port $Port)) { return $false }
        return Test-P005V122BrowserEndpoint -URL $URL
    } catch {
        return $false
    }
}

function Write-P005V122PreflightEvidence {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][hashtable]$Result
    )

    $evidenceRoot = Join-Path $CacheRoot 'evidence\p0-05-v1.2.2'
    $preflightPath = Join-Path $evidenceRoot 'preflight.json'
    $preflightTemporaryPath = Join-Path $evidenceRoot ("preflight.$([guid]::NewGuid().ToString('N')).tmp")
    try {
        $Result | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $preflightTemporaryPath -Encoding utf8
        Move-Item -LiteralPath $preflightTemporaryPath -Destination $preflightPath -Force
    } finally {
        if (Test-Path -LiteralPath $preflightTemporaryPath) {
            Remove-Item -LiteralPath $preflightTemporaryPath -Force -ErrorAction SilentlyContinue
        }
    }
}

$ClusterName = Assert-P005V122ClusterName -Name $ClusterName
$localEnvironmentLoader = Join-Path $PSScriptRoot 'p0-05-v122-env.ps1'
if (-not (Test-Path -LiteralPath $localEnvironmentLoader -PathType Leaf)) { throw 'BLOCKED_LOCAL_ENV_LOADER' }
. $localEnvironmentLoader
$localEnvironmentPath = Join-Path (Get-P005V122WorktreeRoot) 'deploy\agentteams\v1.2.2\.env.local'
Import-P005V122LocalEnvironment -Path $localEnvironmentPath
$runPreflight = {
    $worktreeRoot = Get-P005V122WorktreeRoot
    $repoRoot = Get-P005V122RepoRoot -WorktreeRoot $worktreeRoot
    $cacheRoot = Initialize-P005V122Cache -RepoRoot $repoRoot
    $contractState = Get-P005V122ContractState -WorktreeRoot $worktreeRoot
    $sourceState = if ($contractState.State -eq 'READY') {
        Get-P005V122SourceState -RepoRoot $repoRoot -Contract $contractState.Contract
    } else {
        @{ State = 'BLOCKED_UPSTREAM_CONTRACT'; UpstreamRoot = (Join-Path $repoRoot '.haowork\cache\upstream\AgentTeams-v1.2.2') }
    }
    $chartRoot = Join-Path $sourceState.UpstreamRoot 'helm\agentteams'
    $helm = Resolve-P005V122Executable -WorktreeRoot $worktreeRoot -Name 'helm'
    $kind = Resolve-P005V122Executable -WorktreeRoot $worktreeRoot -Name 'kind'
    $kubectl = Resolve-P005V122Executable -WorktreeRoot $worktreeRoot -Name 'kubectl'
    $docker = Resolve-P005V122Executable -WorktreeRoot $worktreeRoot -Name 'docker'
    $git = Resolve-P005V122Executable -WorktreeRoot $worktreeRoot -Name 'git'
    $freeGiB = [math]::Round(((Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='E:'").FreeSpace / 1GB), 1)
    $memoryGiB = Get-P005V122MemoryGiB
    $dockerStorage = Get-P005V122DockerStorageEvidence
    $browserEndpoints = @(Get-P005V122BrowserEndpoints | ForEach-Object {
        [ordered]@{
            zone = $_.zone
            url = $_.url
            listening = if ($RequireBrowserEndpoint) {
                Test-P005V122ManagedBrowserEndpoint -CacheRoot $cacheRoot -ClusterName $ClusterName -Zone $_.zone -URL $_.url -Port $_.port
            } else {
                $false
            }
        }
    })

    $blocked = @()
    $dockerCgroupVersion = ''
    if ($contractState.State -ne 'READY') { $blocked += $contractState.State }
    if ($sourceState.State -ne 'READY') { $blocked += $sourceState.State }
    if (-not (Test-Path -LiteralPath $chartRoot -PathType Container)) { $blocked += 'BLOCKED_UPSTREAM_CHART' }
    if ($null -eq $helm) { $blocked += 'BLOCKED_HELM' }
    if ($null -eq $git) { $blocked += 'BLOCKED_GIT' }
    if ($freeGiB -lt 40) { $blocked += 'BLOCKED_DISK_E' }
    $runtimeProviderCheck = [ordered]@{ checked = $false; ready = $false }
    if ($Mode -ne 'RenderOnly') {
        if ($null -eq $docker) { $blocked += 'BLOCKED_DOCKER' }
        elseif (-not (Test-P005V122DockerDaemon -Docker $docker)) { $blocked += 'BLOCKED_DOCKER_DAEMON' }
        else {
            $dockerCgroupVersion = Get-P005V122DockerCgroupVersion -Docker $docker
            if ($dockerCgroupVersion -eq '1') { $blocked += 'BLOCKED_CGROUP_V1' }
            elseif ($dockerCgroupVersion -ne '2') { $blocked += 'BLOCKED_DOCKER_CGROUP' }
        }
        if ($null -eq $kind) { $blocked += 'BLOCKED_KIND' }
        if ($null -eq $kubectl) { $blocked += 'BLOCKED_KUBECTL' }
        if ($memoryGiB -lt 8) { $blocked += 'BLOCKED_MEMORY' }
        if ($dockerStorage.status -ne 'READY') { $blocked += 'BLOCKED_DOCKER_STORAGE' }
        if ($contractState.Contract -and -not $contractState.DeploymentImagesReady) { $blocked += 'BLOCKED_IMAGE_DIGEST' }
    }
    if ($Mode -eq 'LocalDualNamespace') {
        $requiredRuntimeNames = @(
            'HAOWORK_P005_PUBLIC_LLM_PROVIDER',
            'HAOWORK_P005_PUBLIC_LLM_API_KEY',
            'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
            'HAOWORK_P005_PUBLIC_LLM_MODEL',
            'HAOWORK_P005_INTERNAL_LLM_PROVIDER',
            'HAOWORK_P005_INTERNAL_LLM_API_KEY',
            'HAOWORK_P005_INTERNAL_LLM_BASE_URL',
            'HAOWORK_P005_INTERNAL_LLM_MODEL'
        )
        foreach ($name in $requiredRuntimeNames) {
            if ([string]::IsNullOrWhiteSpace([string](Get-Item -Path "Env:$name" -ErrorAction SilentlyContinue).Value)) {
                $blocked += 'BLOCKED_RUNTIME_CREDENTIALS'
                break
            }
        }
        $validEgressZones = @()
        foreach ($zone in @('public', 'internal')) {
            if (-not (Test-P005V122ExternalEgressCIDRs -Zone $zone)) {
                $blocked += 'BLOCKED_EGRESS_CIDRS'
            } else {
                $validEgressZones += $zone
            }
        }
        $hasRuntimeInputs = $requiredRuntimeNames |
            ForEach-Object { -not [string]::IsNullOrWhiteSpace([string](Get-Item -Path "Env:$_" -ErrorAction SilentlyContinue).Value) } |
            Where-Object { $_ }
        if (@($hasRuntimeInputs).Count -eq $requiredRuntimeNames.Count -and $validEgressZones.Count -eq 2) {
            $runtimeProviderCheck.checked = $true
            $runtimeProviderCheck.ready = $true
            foreach ($zone in @('public', 'internal')) {
                if (-not (Test-P005V122RuntimeProviderEndpoint -Zone $zone)) {
                    $runtimeProviderCheck.ready = $false
                    $blocked += 'BLOCKED_RUNTIME_PROVIDER_ENDPOINT'
                    break
                }
            }
        }
        if ($RequireBrowserEndpoint -and @($browserEndpoints | Where-Object { -not $_.listening }).Count -gt 0) {
            $blocked += 'BLOCKED_BROWSER_ENDPOINT'
        }
    }

    $result = [ordered]@{
        mode = $Mode
        cluster_name = $ClusterName
        ok = $blocked.Count -eq 0
        blocked = @($blocked | Select-Object -Unique)
        cache_root = $cacheRoot
        contract_lock = $contractState.LockPath
        upstream_root = $sourceState.UpstreamRoot
        chart_root = $chartRoot
        executables = [ordered]@{ docker = $docker; kind = $kind; kubectl = $kubectl; helm = $helm; git = $git }
        resource_checks = [ordered]@{ memory_gib = $memoryGiB; disk_e_free_gib = $freeGiB; docker_storage = $dockerStorage; docker_cgroup_version = $dockerCgroupVersion; runtime_provider = $runtimeProviderCheck; browser_endpoints = $browserEndpoints }
    }
    Write-P005V122PreflightEvidence -CacheRoot $cacheRoot -Result $result
    if ($Json) { $result | ConvertTo-Json -Depth 8 }
    if (-not $result.ok) { throw ($result.blocked -join ',') }
}

Invoke-P005V122WithDeploymentLock -ClusterName $ClusterName -Action {
    # .NET named mutexes are reentrant on the owning thread, so this also
    # remains safe when up has already acquired the deployment lifecycle lock.
    Invoke-P005V122WithEvidenceLock -Action $runPreflight
}
