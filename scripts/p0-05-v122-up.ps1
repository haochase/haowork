[CmdletBinding()]
param(
    [ValidateSet('RenderOnly', 'ClusterOnly', 'LocalDualNamespace')]
    [string]$Mode = 'RenderOnly',
    [string]$ClusterName = 'haowork-p005-v122'
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

function Initialize-P005V122Cache {
    param([Parameter(Mandatory)][string]$RepoRoot)

    $cacheRoot = Join-Path $RepoRoot '.haowork\cache'
    foreach ($name in @('tmp', 'go', 'gomod', 'npm', 'helm', 'helm\config', 'helm\data', 'kind', 'evidence\p0-05-v1.2.2')) {
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
    $env:KUBECONFIG = Join-Path $cacheRoot "kind\$ClusterName.kubeconfig"
    return $cacheRoot
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

function ConvertTo-P005V122YamlScalar {
    param([AllowEmptyString()][Parameter(Mandatory)][string]$Value)

    # JSON quoted scalars are valid YAML quoted scalars and preserve quotes,
    # backslashes, and line breaks without allowing value-file injection.
    return ($Value | ConvertTo-Json -Compress)
}

function Get-P005V122RuntimeValues {
    param(
        [Parameter(Mandatory)][string]$Zone
    )

    $prefix = "HAOWORK_P005_$($Zone.ToUpperInvariant())"
    $provider = [string](Get-Item -Path "Env:${prefix}_LLM_PROVIDER" -ErrorAction SilentlyContinue).Value
    $apiKey = [string](Get-Item -Path "Env:${prefix}_LLM_API_KEY" -ErrorAction SilentlyContinue).Value
    $baseUrl = [string](Get-Item -Path "Env:${prefix}_LLM_BASE_URL" -ErrorAction SilentlyContinue).Value
    $model = [string](Get-Item -Path "Env:${prefix}_LLM_MODEL" -ErrorAction SilentlyContinue).Value
    if ([string]::IsNullOrWhiteSpace($provider) -or [string]::IsNullOrWhiteSpace($apiKey) -or
        [string]::IsNullOrWhiteSpace($baseUrl) -or [string]::IsNullOrWhiteSpace($model)) {
        throw "BLOCKED_RUNTIME_CREDENTIALS: ${prefix} LLM provider, model, API key, and base URL are required"
    }
    $safeProvider = ConvertTo-P005V122YamlScalar -Value $provider
    $adminPassword = [guid]::NewGuid().ToString('N')
    $minioPassword = [guid]::NewGuid().ToString('N')
    $safeApiKey = ConvertTo-P005V122YamlScalar -Value $apiKey
    $safeBaseUrl = ConvertTo-P005V122YamlScalar -Value $baseUrl
    $safeModel = ConvertTo-P005V122YamlScalar -Value $model
    $safeAdminPassword = ConvertTo-P005V122YamlScalar -Value $adminPassword
    $safeMinioPassword = ConvertTo-P005V122YamlScalar -Value $minioPassword
    return @"
credentials:
  llmProvider: $safeProvider
  defaultModel: $safeModel
  llmApiKey: $safeApiKey
  llmBaseUrl: $safeBaseUrl
  adminPassword: $safeAdminPassword
storage:
  minio:
    auth:
      rootPassword: $safeMinioPassword
"@
}

function Get-P005V122ProviderHost {
    param([Parameter(Mandatory)][string]$BaseURL)

    $uri = $null
    if (-not [Uri]::TryCreate($BaseURL, [UriKind]::Absolute, [ref]$uri) -or
        $uri.Scheme -notin @('http', 'https') -or
        [string]::IsNullOrWhiteSpace($uri.Host) -or
        -not [string]::IsNullOrWhiteSpace($uri.UserInfo)) {
        throw 'BLOCKED_RUNTIME_PROVIDER_URL'
    }
    return $uri.Host
}

function Set-P005V122HigressRouteHostHeader {
    param(
        [Parameter(Mandatory)]$Route,
        [Parameter(Mandatory)][string]$HostName
    )

    if ([string]::IsNullOrWhiteSpace([string]$Route.name) -or [string]::IsNullOrWhiteSpace($HostName)) {
        throw 'BLOCKED_HIGRESS_ROUTE_CONTRACT'
    }
    $headerControl = [pscustomobject][ordered]@{
        enabled = $true
        request = [pscustomobject][ordered]@{
            add = $null
            set = @([pscustomobject][ordered]@{ key = 'Host'; value = $HostName })
            remove = $null
        }
        response = [pscustomobject][ordered]@{ add = $null; set = $null; remove = $null }
    }
    $Route | Add-Member -MemberType NoteProperty -Name headerControl -Value $headerControl -Force
    return $Route
}

function Wait-P005V122LocalTCPPort {
    param(
        [Parameter(Mandatory)][int]$Port,
        [int]$TimeoutSeconds = 20
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $client = $null
        try {
            $client = New-Object System.Net.Sockets.TcpClient
            $pending = $client.BeginConnect('127.0.0.1', $Port, $null, $null)
            if ($pending.AsyncWaitHandle.WaitOne(250) -and $client.Connected) { return }
        } catch {
        } finally {
            if ($null -ne $client) { $client.Dispose() }
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw 'BLOCKED_HIGRESS_CONSOLE_PORT_FORWARD'
}

function Set-P005V122HigressProviderHostHeader {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)]$Zone,
        [Parameter(Mandatory)][string]$ProviderBaseURL,
        [Parameter(Mandatory)][string]$CacheRoot
    )

    $hostName = Get-P005V122ProviderHost -BaseURL $ProviderBaseURL
    $listener = New-Object System.Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    $stdoutPath = Join-Path $CacheRoot "tmp\higress-$($Zone.Name)-console.stdout.log"
    $stderrPath = Join-Path $CacheRoot "tmp\higress-$($Zone.Name)-console.stderr.log"
    $process = $null
    try {
        $process = Start-Process -FilePath $Kubectl -ArgumentList @(
            '--kubeconfig', $env:KUBECONFIG,
            'port-forward', '--namespace', $Zone.Namespace,
            'service/higress-console', "${port}:8080", '--address', '127.0.0.1'
        ) -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
        Wait-P005V122LocalTCPPort -Port $port

        $secretJSON = & $Kubectl --kubeconfig $env:KUBECONFIG get secret higress-console --namespace $Zone.Namespace -o json
        if ($LASTEXITCODE -ne 0) { throw "BLOCKED_HIGRESS_ROUTE_$($Zone.Name.ToUpperInvariant())" }
        $secret = $secretJSON | ConvertFrom-Json
        foreach ($field in @('adminUsername', 'adminPassword')) {
            if ([string]::IsNullOrWhiteSpace([string]$secret.data.$field)) {
                throw "BLOCKED_HIGRESS_ROUTE_$($Zone.Name.ToUpperInvariant())"
            }
        }
        $username = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([string]$secret.data.adminUsername))
        $password = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([string]$secret.data.adminPassword))
        $baseURL = "http://127.0.0.1:$port"
        $loginBody = @{ username = $username; password = $password } | ConvertTo-Json -Compress
        $null = Invoke-WebRequest -Uri "$baseURL/session/login" -Method Post -ContentType 'application/json' -Body $loginBody -SessionVariable session -UseBasicParsing

        $route = $null
        foreach ($attempt in 1..20) {
            try {
                $response = Invoke-WebRequest -Uri "$baseURL/v1/ai/routes/default-ai-route" -WebSession $session -UseBasicParsing
                $route = ($response.Content | ConvertFrom-Json).data
                if ($null -ne $route -and [string]$route.name -ceq 'default-ai-route') { break }
            } catch {
                if ($attempt -eq 20) { throw }
            }
            Start-Sleep -Seconds 1
        }
        if ($null -eq $route -or [string]$route.name -cne 'default-ai-route') {
            throw "BLOCKED_HIGRESS_ROUTE_$($Zone.Name.ToUpperInvariant())"
        }
        $route = Set-P005V122HigressRouteHostHeader -Route $route -HostName $hostName
        $update = Invoke-WebRequest -Uri "$baseURL/v1/ai/routes/default-ai-route" -Method Put -WebSession $session -ContentType 'application/json' -Body ($route | ConvertTo-Json -Depth 30 -Compress) -UseBasicParsing
        if ($update.StatusCode -notin @(200, 201)) { throw "BLOCKED_HIGRESS_ROUTE_$($Zone.Name.ToUpperInvariant())" }
        $updateBody = $update.Content | ConvertFrom-Json
        if ($null -ne $updateBody.success -and -not [bool]$updateBody.success) {
            throw "BLOCKED_HIGRESS_ROUTE_$($Zone.Name.ToUpperInvariant())"
        }
    } catch {
        if ($_.Exception.Message -like 'BLOCKED_*') { throw }
        throw "BLOCKED_HIGRESS_ROUTE_$($Zone.Name.ToUpperInvariant()): $($_.Exception.Message)"
    } finally {
        if ($null -ne $process) {
            if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
            $process.Dispose()
        }
    }
}

function Get-P005V122RenderOnlyValues {
    # The upstream template requires non-empty credentials.llmApiKey even for
    # helm template. These fixed values are synthetic render inputs, never
    # deployed, and are written only under the ignored project cache.
    return @"
credentials:
  llmApiKey: "render-only-not-a-runtime-secret"
  llmBaseUrl: "http://127.0.0.1:1"
  adminPassword: "render-only-not-a-runtime-secret"
storage:
  minio:
    auth:
      rootPassword: "render-only-not-a-runtime-secret"
"@
}

function Write-P005V122ResolvedValues {
    param(
        [Parameter(Mandatory)][string]$BaseValuesPath,
        [Parameter(Mandatory)][string]$OutputPath,
        [string]$RuntimeValues
    )

    $base = Get-Content -LiteralPath $BaseValuesPath -Raw -Encoding utf8
    if ([string]::IsNullOrWhiteSpace($RuntimeValues)) {
        $base | Set-Content -LiteralPath $OutputPath -Encoding utf8
    } else {
        ($base.TrimEnd() + "`n" + $RuntimeValues.Trim() + "`n") | Set-Content -LiteralPath $OutputPath -Encoding utf8
    }
}

function Write-P005V122Evidence {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Text
    )
    $Text | Set-Content -LiteralPath $Path -Encoding utf8
}

function Get-P005V122BrowserPortForwards {
    return @(
        [ordered]@{ Zone = 'public'; Namespace = 'haowork-public'; Port = 18080; URL = 'http://127.0.0.1:18080' },
        [ordered]@{ Zone = 'internal'; Namespace = 'haowork-internal'; Port = 18082; URL = 'http://127.0.0.1:18082' }
    )
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

function Write-P005V122BrowserPortForwardState {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][object[]]$Entries
    )

    $statePath = Get-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -ClusterName $ClusterName
    Assert-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -StatePath $statePath
    $temporaryPath = "$statePath.$([guid]::NewGuid().ToString('N')).tmp"
    try {
        [ordered]@{ schema_version = 1; entries = @($Entries) } |
            ConvertTo-Json -Depth 5 |
            Set-Content -LiteralPath $temporaryPath -Encoding utf8
        Move-Item -LiteralPath $temporaryPath -Destination $statePath -Force
    } finally {
        if (Test-Path -LiteralPath $temporaryPath) {
            Remove-Item -LiteralPath $temporaryPath -Force -ErrorAction SilentlyContinue
        }
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

    $entries = @(Read-P005V122BrowserPortForwardState -CacheRoot $CacheRoot -ClusterName $ClusterName |
        Where-Object { [string]$_.zone -eq $Zone })
    if ($entries.Count -ne 1) { return $false }
    $entry = $entries[0]
    try {
        $processId = [int]$entry.pid
        $processStartTimeUtc = [string]$entry.process_start_time_utc
        $kubeconfigPath = [string]$entry.kubeconfig_path
        $kubectlPath = [string]$entry.kubectl_path
        $cacheDirectory = [string]$entry.cache_directory
        $runToken = [string]$entry.run_token
        if ([string]::IsNullOrWhiteSpace($processStartTimeUtc) -or [string]::IsNullOrWhiteSpace($kubeconfigPath)) { return $false }
        if (-not (Test-P005V122ManagedBrowserPortForward -ProcessId $processId -Namespace ([string]$entry.namespace) -Port $Port -ProcessStartTimeUtc $processStartTimeUtc -KubeconfigPath $kubeconfigPath -KubectlPath $kubectlPath -CacheDirectory $cacheDirectory -RunToken $runToken)) { return $false }
        if (-not (Test-P005V122ProcessOwnsBrowserPort -ProcessId $processId -Port $Port)) { return $false }
        return Test-P005V122BrowserEndpoint -URL $URL
    } catch {
        return $false
    }
}

function Wait-P005V122BrowserEndpoint {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName,
        [Parameter(Mandatory)][hashtable]$Endpoint,
        [int]$TimeoutSeconds = 20
    )

    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (Test-P005V122ManagedBrowserEndpoint -CacheRoot $CacheRoot -ClusterName $ClusterName -Zone $Endpoint.Zone -URL $Endpoint.URL -Port $Endpoint.Port) { return $true }
        Start-Sleep -Milliseconds 250
    }
    return $false
}

function Remove-P005V122ManagedBrowserCacheDirectory {
    param([Parameter(Mandatory)][string]$Path)

    for ($attempt = 1; $attempt -le 3; $attempt++) {
        if (-not (Test-Path -LiteralPath $Path)) { return }
        try {
            Remove-Item -LiteralPath $Path -Recurse -Force -ErrorAction Stop
        } catch [System.IO.DirectoryNotFoundException] {
            # The kubectl process can remove its cache while cleanup is traversing it.
        } catch [System.Management.Automation.ItemNotFoundException] {
            # Treat an already-removed managed cache as a successful cleanup.
        } catch {
            throw 'BLOCKED_BROWSER_CACHE_CLEANUP'
        }
        if (-not (Test-Path -LiteralPath $Path)) { return }
        Start-Sleep -Milliseconds 50
    }
    throw 'BLOCKED_BROWSER_CACHE_CLEANUP'
}

function Stop-P005V122BrowserPortForward {
    param(
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    $entries = Read-P005V122BrowserPortForwardState -CacheRoot $CacheRoot -ClusterName $ClusterName
    foreach ($entry in $entries) {
        $processId = [int]$entry.pid
        $zone = [string]$entry.zone
        $processStartTimeUtc = [string]$entry.process_start_time_utc
        $kubeconfigPath = [string]$entry.kubeconfig_path
        $kubectlPath = [string]$entry.kubectl_path
        $cacheDirectory = [string]$entry.cache_directory
        $runToken = [string]$entry.run_token
        $safeCacheDirectory = Test-P005V122ManagedBrowserCacheDirectory -CacheRoot $CacheRoot -ClusterName $ClusterName -Zone $zone -CacheDirectory $cacheDirectory -RunToken $runToken
        if ($safeCacheDirectory -and
            -not [string]::IsNullOrWhiteSpace($processStartTimeUtc) -and
            -not [string]::IsNullOrWhiteSpace($kubeconfigPath) -and
            -not [string]::IsNullOrWhiteSpace($kubectlPath) -and
            -not [string]::IsNullOrWhiteSpace($cacheDirectory) -and
            -not [string]::IsNullOrWhiteSpace($runToken) -and
            (Test-P005V122ManagedBrowserPortForward -ProcessId $processId -Namespace ([string]$entry.namespace) -Port ([int]$entry.port) -ProcessStartTimeUtc $processStartTimeUtc -KubeconfigPath $kubeconfigPath -KubectlPath $kubectlPath -CacheDirectory $cacheDirectory -RunToken $runToken)) {
            try {
                Stop-Process -Id $processId -Force -ErrorAction Stop
            } catch {
                throw 'BLOCKED_BROWSER_PORT_FORWARD_STOP'
            }
        }
        if ($safeCacheDirectory -and (Test-Path -LiteralPath $cacheDirectory)) {
            Remove-P005V122ManagedBrowserCacheDirectory -Path $cacheDirectory
        }
    }
    $statePath = Get-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -ClusterName $ClusterName
    Assert-P005V122BrowserPortForwardStatePath -CacheRoot $CacheRoot -StatePath $statePath
    if (Test-Path -LiteralPath $statePath) {
        Remove-Item -LiteralPath $statePath -Force -ErrorAction Stop
    }
}

function Start-P005V122BrowserPortForward {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    Stop-P005V122BrowserPortForward -CacheRoot $CacheRoot -ClusterName $ClusterName
    $entries = @()
    try {
        foreach ($endpoint in Get-P005V122BrowserPortForwards) {
            $runToken = [guid]::NewGuid().ToString('N')
            $cacheDirectory = Join-Path $CacheRoot "kind\$ClusterName.$($endpoint.Zone).browser-port-forward-$runToken.kubectl-cache"
            $stdoutPath = Join-Path $CacheRoot "kind\$ClusterName.$($endpoint.Zone).browser-port-forward.stdout.log"
            $stderrPath = Join-Path $CacheRoot "kind\$ClusterName.$($endpoint.Zone).browser-port-forward.stderr.log"
            $process = Start-Process -FilePath $Kubectl -ArgumentList @(
                '--kubeconfig', $env:KUBECONFIG,
                '--cache-dir', $cacheDirectory,
                'port-forward',
                '--address', '127.0.0.1',
                '--namespace', $endpoint.Namespace,
                'service/higress-gateway',
                "$($endpoint.Port):80"
            ) -WorkingDirectory $CacheRoot -PassThru -WindowStyle Hidden `
                -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
            $processRecord = $null
            for ($attempt = 0; $attempt -lt 20 -and $null -eq $processRecord; $attempt++) {
                $processRecord = Get-CimInstance Win32_Process -Filter "ProcessId = $($process.Id)" -ErrorAction SilentlyContinue
                if ($null -eq $processRecord) { Start-Sleep -Milliseconds 100 }
            }
            $processStartTimeUtc = if ($null -eq $processRecord) { $null } else { Get-P005V122ProcessStartTimeUtc -Process $processRecord }
            if ([string]::IsNullOrWhiteSpace($processStartTimeUtc)) { throw "BLOCKED_BROWSER_ENDPOINT_$($endpoint.Zone.ToUpperInvariant())" }
            $entries += [ordered]@{
                pid = $process.Id
                process_start_time_utc = $processStartTimeUtc
                kubeconfig_path = $env:KUBECONFIG
                kubectl_path = $Kubectl
                cache_directory = $cacheDirectory
                run_token = $runToken
                zone = $endpoint.Zone
                namespace = $endpoint.Namespace
                port = $endpoint.Port
                url = $endpoint.URL
                stdout_log = $stdoutPath
                stderr_log = $stderrPath
            }
            Write-P005V122BrowserPortForwardState -CacheRoot $CacheRoot -ClusterName $ClusterName -Entries $entries
            if (-not (Wait-P005V122BrowserEndpoint -CacheRoot $CacheRoot -ClusterName $ClusterName -Endpoint $endpoint)) {
                throw "BLOCKED_BROWSER_ENDPOINT_$($endpoint.Zone.ToUpperInvariant())"
            }
        }
    } catch {
        Stop-P005V122BrowserPortForward -CacheRoot $CacheRoot -ClusterName $ClusterName
        throw
    }
}

function Copy-P005V122ChartToCache {
    param(
        [Parameter(Mandatory)][string]$SourcePath,
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$Helm
    )

    $chartCache = Join-Path $CacheRoot 'helm\agentteams-v1.2.2'
    $cacheRootFullPath = [IO.Path]::GetFullPath($CacheRoot)
    $chartCacheFullPath = [IO.Path]::GetFullPath($chartCache)
    $expectedChartCacheFullPath = [IO.Path]::GetFullPath((Join-Path $CacheRoot 'helm\agentteams-v1.2.2'))
    if ($chartCacheFullPath -ne $expectedChartCacheFullPath -or
        -not $chartCacheFullPath.StartsWith($cacheRootFullPath + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'BLOCKED_CHART_CACHE_PATH'
    }
    $chartCacheParent = Split-Path -Parent $chartCache
    $transactionId = [guid]::NewGuid().ToString('N')
    $stagingChart = Join-Path $chartCacheParent "agentteams-v1.2.2.stage-$transactionId"
    $backupChart = Join-Path $chartCacheParent "agentteams-v1.2.2.backup-$transactionId"
    foreach ($path in @($stagingChart, $backupChart)) {
        $fullPath = [IO.Path]::GetFullPath($path)
        if (-not $fullPath.StartsWith($cacheRootFullPath + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'BLOCKED_CHART_CACHE_PATH'
        }
    }

    $published = $false
    try {
        Copy-Item -LiteralPath $SourcePath -Destination $stagingChart -Recurse -Force
        Initialize-P005V122ChartDependencies -Helm $Helm -ChartPath $stagingChart
        if (-not (Test-Path -LiteralPath (Join-Path $stagingChart 'charts\higress-2.2.1.tgz') -PathType Leaf)) {
            throw 'BLOCKED_HELM_DEPENDENCY'
        }

        if (Test-Path -LiteralPath $chartCache) {
            Move-Item -LiteralPath $chartCache -Destination $backupChart -ErrorAction Stop
        }
        try {
            Move-Item -LiteralPath $stagingChart -Destination $chartCache -ErrorAction Stop
            $published = $true
        } catch {
            if (Test-Path -LiteralPath $backupChart -PathType Container -and -not (Test-Path -LiteralPath $chartCache)) {
                Move-Item -LiteralPath $backupChart -Destination $chartCache -ErrorAction Stop
            }
            throw 'BLOCKED_CHART_CACHE_PUBLISH'
        }
        if (Test-Path -LiteralPath $backupChart) {
            Remove-Item -LiteralPath $backupChart -Recurse -Force -ErrorAction Stop
        }
        return $chartCache
    } finally {
        if (Test-Path -LiteralPath $stagingChart) {
            Remove-Item -LiteralPath $stagingChart -Recurse -Force -ErrorAction SilentlyContinue
        }
        if ($published -and (Test-Path -LiteralPath $backupChart)) {
            Remove-Item -LiteralPath $backupChart -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

function Initialize-P005V122ChartDependencies {
    param(
        [Parameter(Mandatory)][string]$Helm,
        [Parameter(Mandatory)][string]$ChartPath
    )

    & $Helm repo add higress https://higress.io/helm-charts --force-update | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_HELM_REPOSITORY' }
    & $Helm dependency build $ChartPath | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_HELM_DEPENDENCY' }
}

function Invoke-P005V122WithChartCacheLock {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [int]$TimeoutSeconds = 300
    )

    $mutex = New-Object System.Threading.Mutex($false, 'Haowork.P005.AgentTeams.V122.ChartCache')
    $hasLock = $false
    try {
        try {
            $hasLock = $mutex.WaitOne([TimeSpan]::FromSeconds($TimeoutSeconds))
        } catch [System.Threading.AbandonedMutexException] {
            $hasLock = $true
        }
        if (-not $hasLock) { throw 'BLOCKED_CHART_CACHE_LOCK_TIMEOUT' }
        & $Action
    } finally {
        if ($hasLock) { $mutex.ReleaseMutex() }
        $mutex.Dispose()
    }
}

function ConvertTo-P005V122SafeManifest {
    param([Parameter(Mandatory)][string]$Manifest)

    $documents = [regex]::Split($Manifest, '(?m)^---\s*\r?\n')
    $safeDocuments = foreach ($document in $documents) {
        if ($document -match '(?m)^kind:\s*Secret\s*$') {
            $metadata = [regex]::Match($document, '(?ms)^apiVersion:.*?^type:.*?\r?\n')
            if ($metadata.Success) {
                $metadata.Value + "stringData:`n  <redacted>: `<redacted>`n"
            } else {
                "apiVersion: v1`nkind: Secret`nstringData:`n  <redacted>: `<redacted>`n"
            }
        } else {
            $document
        }
    }
    return ($safeDocuments -join "---`n")
}

function Get-P005V122ExternalEgressCIDRs {
    param([Parameter(Mandatory)][string]$Zone)

    $prefix = "HAOWORK_P005_$($Zone.ToUpperInvariant())"
    $raw = [string](Get-Item -Path "Env:${prefix}_EGRESS_CIDRS" -ErrorAction SilentlyContinue).Value
    if ([string]::IsNullOrWhiteSpace($raw)) { return @() }
    $cidrs = @($raw -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
    foreach ($cidr in $cidrs) {
        if (-not (Test-P005V122CIDR -Value $cidr)) {
            throw "BLOCKED_EGRESS_CIDRS_$($Zone.ToUpperInvariant())"
        }
    }
    return $cidrs
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

function Write-P005V122KubernetesAPIPolicy {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][hashtable]$Zone,
        [Parameter(Mandatory)][string]$OutputPath
    )

    $apiIP = (& $Kubectl get service kubernetes --namespace default -o jsonpath='{.spec.clusterIP}').Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($apiIP)) { throw 'BLOCKED_KUBERNETES_API_SERVICE' }
    $ip = $null
    if (-not [System.Net.IPAddress]::TryParse($apiIP, [ref]$ip)) { throw 'BLOCKED_KUBERNETES_API_SERVICE' }
    $cidr = if ($ip.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork) { "$apiIP/32" } else { "$apiIP/128" }
    $endpointJSON = (& $Kubectl get endpoints kubernetes --namespace default -o json) -join "`n"
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($endpointJSON)) { throw 'BLOCKED_KUBERNETES_API_ENDPOINTS' }
    try {
        $endpointObject = $endpointJSON | ConvertFrom-Json
    } catch {
        throw 'BLOCKED_KUBERNETES_API_ENDPOINTS'
    }
    $endpointRules = New-Object Collections.Generic.List[string]
    $seenEndpoints = New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
    foreach ($subset in @($endpointObject.subsets)) {
        foreach ($port in @($subset.ports)) {
            $portNumber = [int]$port.port
            if ($portNumber -le 0 -or $portNumber -gt 65535 -or ([string]$port.protocol -notin @('', 'TCP'))) { continue }
            foreach ($address in @($subset.addresses)) {
                $endpointIP = $null
                if (-not [System.Net.IPAddress]::TryParse([string]$address.ip, [ref]$endpointIP)) { continue }
                $endpointCIDR = if ($endpointIP.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork) { "$($endpointIP.IPAddressToString)/32" } else { "$($endpointIP.IPAddressToString)/128" }
                $endpointKey = "$endpointCIDR`:$portNumber"
                if (-not $seenEndpoints.Add($endpointKey)) { continue }
                $endpointRules.Add("    - to:`n        - ipBlock:`n            cidr: $endpointCIDR`n      ports:`n        - protocol: TCP`n          port: $portNumber")
            }
        }
    }
    if ($endpointRules.Count -eq 0) { throw 'BLOCKED_KUBERNETES_API_ENDPOINTS' }
    $renderedEndpointRules = $endpointRules -join "`n"
    @"
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: haowork-$($Zone.Name)-allow-kubernetes-api
  namespace: $($Zone.Namespace)
  labels:
    haowork.io/zone: $($Zone.Name)
spec:
  podSelector: {}
  policyTypes:
    - Egress
  egress:
    - to:
        - ipBlock:
            cidr: $cidr
      ports:
        - protocol: TCP
          port: 443
$renderedEndpointRules
"@ | Set-Content -LiteralPath $OutputPath -Encoding utf8
}

function Write-P005V122ExternalEgressPolicy {
    param(
        [Parameter(Mandatory)][hashtable]$Zone,
        [Parameter(Mandatory)][string[]]$CIDRs,
        [Parameter(Mandatory)][string]$OutputPath
    )

    if ($CIDRs.Count -eq 0) { return $false }
    $rules = @($CIDRs | ForEach-Object { "    - to:`n        - ipBlock:`n            cidr: $_" }) -join "`n"
    @"
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: haowork-$($Zone.Name)-allow-explicit-external-egress
  namespace: $($Zone.Namespace)
  labels:
    haowork.io/zone: $($Zone.Name)
spec:
  podSelector: {}
  policyTypes:
    - Egress
  egress:
$rules
"@ | Set-Content -LiteralPath $OutputPath -Encoding utf8
    return $true
}

function Get-P005V122KindClusters {
    param([Parameter(Mandatory)][string]$Kind)

    $previousErrorActionPreference = $ErrorActionPreference
    try {
        # Kind writes the empty-cluster notice to stderr with a zero exit code.
        # Capture it without treating a normal empty state as a failed query.
        $ErrorActionPreference = 'Continue'
        $clusters = @(& $Kind get clusters 2>&1 | ForEach-Object { $_.ToString().Trim() })
        $kindQueryExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($kindQueryExitCode -ne 0) { throw 'BLOCKED_KIND_QUERY' }
    return @($clusters | Where-Object { $_ -and $_ -ne 'No kind clusters found.' })
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

function Get-P005V122HelmReleaseRevision {
    param(
        [Parameter(Mandatory)][string]$Helm,
        [Parameter(Mandatory)][hashtable]$Zone
    )

    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $statusJson = & $Helm status $Zone.Release --namespace $Zone.Namespace -o json 2>&1
        if ($LASTEXITCODE -ne 0) {
            $statusText = $statusJson -join "`n"
            if ($statusText -match '(?i)release:\s*not found|not found') { return $null }
            throw "BLOCKED_HELM_STATUS_$($Zone.Name.ToUpperInvariant())"
        }
        $status = ($statusJson -join "`n") | ConvertFrom-Json
        $revision = [int]$status.version
        if ($revision -le 0) { throw "BLOCKED_HELM_STATUS_$($Zone.Name.ToUpperInvariant())" }
        return $revision
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
}

function Remove-P005V122OwnedNamespace {
    param(
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][hashtable]$Zone
    )

    & $Kubectl delete namespace $Zone.Namespace --ignore-not-found | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_ROLLBACK_NAMESPACE_$($Zone.Name.ToUpperInvariant())" }
    & $Kubectl wait --for=delete "namespace/$($Zone.Namespace)" --timeout=120s | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_ROLLBACK_NAMESPACE_WAIT_$($Zone.Name.ToUpperInvariant())" }
}

function Invoke-P005V122Rollback {
    param(
        [Parameter(Mandatory)][hashtable]$Transaction,
        [Parameter(Mandatory)][string]$Helm,
        [Parameter(Mandatory)][string]$Kubectl,
        [Parameter(Mandatory)][string]$Kind,
        [Parameter(Mandatory)][string]$CacheRoot,
        [Parameter(Mandatory)][string]$ClusterName
    )

    $failures = @()
    $createdReleases = @($Transaction.created_releases)
    [array]::Reverse($createdReleases)
    foreach ($zone in $createdReleases) {
        & $Helm uninstall $zone.Release --namespace $zone.Namespace --ignore-not-found --wait --timeout 5m | Out-Null
        if ($LASTEXITCODE -ne 0) { $failures += "helm:$($zone.Name)" }
    }
    $updatedReleases = @($Transaction.updated_releases)
    [array]::Reverse($updatedReleases)
    foreach ($release in $updatedReleases) {
        & $Helm rollback $release.zone.Release $release.previous_revision --namespace $release.zone.Namespace --wait --timeout 5m | Out-Null
        if ($LASTEXITCODE -ne 0) { $failures += "helm-rollback:$($release.zone.Name)" }
    }
    $deletedNamespaceNames = @()
    $createdNamespaces = @($Transaction.created_namespaces)
    [array]::Reverse($createdNamespaces)
    foreach ($zone in $createdNamespaces) {
        try {
            Remove-P005V122OwnedNamespace -Kubectl $Kubectl -Zone $zone
            $deletedNamespaceNames += $zone.Namespace
        } catch {
            $failures += "namespace:$($zone.Name)"
        }
    }
    if ($Transaction.ownership_verified -or $Transaction.created_cluster) {
        try {
            Stop-P005V122BrowserPortForward -CacheRoot $CacheRoot -ClusterName $ClusterName
        } catch {
            $failures += 'browser-port-forward'
        }
    }
    if ($Transaction.created_cluster) {
        & $Kind delete cluster --name $ClusterName | Out-Null
        if ($LASTEXITCODE -ne 0) { $failures += 'kind' }
        if ($failures.Count -eq 0) { Remove-P005V122DeploymentOwnership -CacheRoot $CacheRoot -ClusterName $ClusterName }
    } elseif ($null -ne $Transaction.ownership -and $deletedNamespaceNames.Count -gt 0) {
        $Transaction.ownership.namespaces = @($Transaction.ownership.namespaces | Where-Object {
            [string]$_.name -notin $deletedNamespaceNames
        })
        Write-P005V122DeploymentOwnership -CacheRoot $CacheRoot -ClusterName $ClusterName -Ownership $Transaction.ownership
    }
    if ($failures.Count -gt 0) {
        throw ('BLOCKED_PARTIAL_DEPLOYMENT_ROLLBACK:' + ($failures -join ','))
    }
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
$localEnvironmentLoader = Join-Path $PSScriptRoot 'p0-05-v122-env.ps1'
if (-not (Test-Path -LiteralPath $localEnvironmentLoader -PathType Leaf)) { throw 'BLOCKED_LOCAL_ENV_LOADER' }
. $localEnvironmentLoader
$localEnvironmentPath = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\.env.local'
Import-P005V122LocalEnvironment -Path $localEnvironmentPath
$repoRoot = Get-P005V122RepoRoot -WorktreeRoot $worktreeRoot
$cacheRoot = Initialize-P005V122Cache -RepoRoot $repoRoot
$preflightPath = Join-Path $worktreeRoot 'scripts\p0-05-v122-preflight.ps1'

$helm = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'helm'
$upstreamRoot = Assert-P005V122LockedOfficialSource -WorktreeRoot $worktreeRoot -RepoRoot $repoRoot
$deploymentRoot = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2'
$evidenceRoot = Join-Path $cacheRoot 'evidence\p0-05-v1.2.2'
$chartRoot = Join-Path $upstreamRoot 'helm\agentteams'

if (-not (Test-Path -LiteralPath $chartRoot -PathType Container)) { throw 'BLOCKED_UPSTREAM_CHART' }

$zones = @(
    @{ Name = 'public'; Namespace = 'haowork-public'; Release = 'haowork-public-agentteams'; BaseValues = (Join-Path $deploymentRoot 'public-values.yaml'); Policy = (Join-Path $deploymentRoot 'public-network-policy.yaml'); RuntimeSecret = 'haowork-public-agentteams-runtime-env' },
    @{ Name = 'internal'; Namespace = 'haowork-internal'; Release = 'haowork-internal-agentteams'; BaseValues = (Join-Path $deploymentRoot 'internal-values.yaml'); Policy = (Join-Path $deploymentRoot 'internal-network-policy.yaml'); RuntimeSecret = 'haowork-internal-agentteams-runtime-env' }
)

Invoke-P005V122WithDeploymentLock -ClusterName $ClusterName -Action {
    Invoke-P005V122WithChartCacheLock -Action {
        & $preflightPath -Mode $Mode -ClusterName $ClusterName -SkipDeploymentLock
        $upstreamRoot = Assert-P005V122LockedOfficialSource -WorktreeRoot $worktreeRoot -RepoRoot $repoRoot
        $chartRoot = Join-Path $upstreamRoot 'helm\agentteams'
        if (-not (Test-Path -LiteralPath $chartRoot -PathType Container)) { throw 'BLOCKED_UPSTREAM_CHART' }
        $chartCache = Copy-P005V122ChartToCache -SourcePath $chartRoot -CacheRoot $cacheRoot -Helm $helm

    foreach ($zone in $zones) {
        $resolvedPath = Join-Path $evidenceRoot "$($zone.Name)-values.render.yaml"
        Write-P005V122ResolvedValues -BaseValuesPath $zone.BaseValues -OutputPath $resolvedPath
        $renderInputPath = Join-Path $cacheRoot "tmp\$($zone.Name)-values.render-input.yaml"
        $renderOverlayPath = Join-Path $cacheRoot "tmp\$($zone.Name)-values.render-overlay.yaml"
        Write-P005V122ResolvedValues -BaseValuesPath $zone.BaseValues -OutputPath $renderInputPath
        Get-P005V122RenderOnlyValues | Set-Content -LiteralPath $renderOverlayPath -Encoding utf8
        $manifestPath = Join-Path $evidenceRoot "$($zone.Name)-rendered.yaml"
        $unsafeManifestPath = Join-Path $cacheRoot "tmp\$($zone.Name)-rendered.unsafe.yaml"
        try {
            & $helm template $zone.Release $chartCache --namespace $zone.Namespace --values $renderInputPath --values $renderOverlayPath --include-crds | Out-File -LiteralPath $unsafeManifestPath -Encoding utf8
            if ($LASTEXITCODE -ne 0) { throw "BLOCKED_HELM_RENDER_$($zone.Name.ToUpperInvariant())" }
        } finally {
            Remove-Item -LiteralPath $renderInputPath, $renderOverlayPath -Force -ErrorAction SilentlyContinue
        }
        $rendered = Get-Content -LiteralPath $unsafeManifestPath -Raw -Encoding utf8
        ConvertTo-P005V122SafeManifest -Manifest $rendered | Set-Content -LiteralPath $manifestPath -Encoding utf8
        foreach ($required in @('kind: CustomResourceDefinition', 'kind: Deployment', 'kind: StatefulSet', 'kind: Secret', 'AGENTTEAMS_MANAGER_SPEC')) {
            if ($rendered -notmatch [regex]::Escape($required)) { throw "BLOCKED_RENDER_CONTRACT_$($zone.Name.ToUpperInvariant()): missing $required" }
        }
        if ($rendered -notmatch [regex]::Escape("name: $($zone.RuntimeSecret)")) {
            throw "BLOCKED_RENDER_SECRET_IDENTITY_$($zone.Name.ToUpperInvariant())"
        }
        if ($rendered -notmatch [regex]::Escape("name: haowork-$($zone.Name)-higress")) {
            throw "BLOCKED_RENDER_HIGRESS_IDENTITY_$($zone.Name.ToUpperInvariant())"
        }
        if ($rendered -match 'AGENTTEAMS_LLM_API_KEY:\s+[^"\s]' -or $rendered -match 'sk-[A-Za-z0-9_-]{12,}') {
            throw "BLOCKED_RENDER_SECRET_LEAK_$($zone.Name.ToUpperInvariant())"
        }
    }

        if ($Mode -eq 'RenderOnly') {
            Write-P005V122Evidence -Path (Join-Path $evidenceRoot 'render-only.txt') -Text 'PASS RenderOnly: both official AgentTeams v1.2.2 chart renders completed without runtime credentials.'
            return
        }

        $kind = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kind'
        $kubectl = Resolve-P005V122Executable -RepoRoot $repoRoot -Name 'kubectl'
        $transaction = @{
            created_cluster = $false
            created_namespaces = @()
            created_releases = @()
            updated_releases = @()
            ownership = $null
            ownership_verified = $false
        }
        try {
            $kindClusters = Get-P005V122KindClusters -Kind $kind
            $clusterExisted = @($kindClusters | Where-Object { $_ -eq $ClusterName }).Count -gt 0
            if (-not $clusterExisted) {
                & $kind create cluster --name $ClusterName --kubeconfig $env:KUBECONFIG
                if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_KIND_CREATE' }
                $transaction.created_cluster = $true
            }
            & $kind export kubeconfig --name $ClusterName --kubeconfig $env:KUBECONFIG | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'BLOCKED_KIND_KUBECONFIG' }

            $clusterIdentity = Get-P005V122ClusterIdentity -Kubectl $kubectl
            $kubeContext = Get-P005V122KubeContext -Kubectl $kubectl
            $ownership = Get-P005V122DeploymentOwnership -CacheRoot $cacheRoot -ClusterName $ClusterName
            if ($clusterExisted) {
                if ($null -eq $ownership -or -not (Test-P005V122ClusterOwnership -Ownership $ownership -ClusterName $ClusterName -ClusterIdentity $clusterIdentity -KubeContext $kubeContext)) {
                    throw 'BLOCKED_CLUSTER_OWNERSHIP'
                }
            } else {
                if ($null -ne $ownership) {
                    throw 'BLOCKED_CLUSTER_OWNERSHIP'
                }
                $ownership = [ordered]@{
                    schema_version = 1
                    cluster_name = $ClusterName
                    cluster_identity = $clusterIdentity
                    kube_context = $kubeContext
                    cluster_created_by_haowork = $true
                    deployment_id = [guid]::NewGuid().ToString('N')
                    namespaces = @()
                }
                Write-P005V122DeploymentOwnership -CacheRoot $cacheRoot -ClusterName $ClusterName -Ownership $ownership
            }
            $transaction.ownership = $ownership
            $transaction.ownership_verified = $true

            if ($Mode -eq 'ClusterOnly') {
                Write-P005V122Evidence -Path (Join-Path $evidenceRoot 'cluster-only.txt') -Text "PASS ClusterOnly: Kind cluster $ClusterName exists and its Haowork ownership is verified."
                return
            }

            foreach ($zone in $zones) {
                $namespaceObject = Get-P005V122NamespaceObject -Kubectl $kubectl -Namespace $zone.Namespace
                if ($null -eq $namespaceObject) {
                    if (@($ownership.namespaces | Where-Object { [string]$_.name -ceq $zone.Namespace }).Count -ne 0) {
                        throw "BLOCKED_NAMESPACE_OWNERSHIP_$($zone.Name.ToUpperInvariant())"
                    }
                    & $kubectl create namespace $zone.Namespace | Out-Null
                    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_NAMESPACE_CREATE_$($zone.Name.ToUpperInvariant())" }
                    $transaction.created_namespaces += $zone
                    $namespaceObject = Get-P005V122NamespaceObject -Kubectl $kubectl -Namespace $zone.Namespace
                    if ($null -eq $namespaceObject) { throw "BLOCKED_NAMESPACE_CREATE_$($zone.Name.ToUpperInvariant())" }
                    & $kubectl label namespace $zone.Namespace "haowork.io/zone=$($zone.Name)" "haowork.io/p005-owner=$($ownership.deployment_id)" --overwrite | Out-Null
                    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_NAMESPACE_LABEL_$($zone.Name.ToUpperInvariant())" }
                    $ownership.namespaces = @($ownership.namespaces) + @([ordered]@{ name = $zone.Namespace; zone = $zone.Name; uid = [string]$namespaceObject.metadata.uid })
                    Write-P005V122DeploymentOwnership -CacheRoot $cacheRoot -ClusterName $ClusterName -Ownership $ownership
                } elseif (-not (Test-P005V122NamespaceOwnership -Ownership $ownership -NamespaceObject $namespaceObject -Namespace $zone.Namespace -Zone $zone.Name)) {
                    throw "BLOCKED_NAMESPACE_OWNERSHIP_$($zone.Name.ToUpperInvariant())"
                }

                & $kubectl apply -f $zone.Policy | Out-Null
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_NETWORK_POLICY_$($zone.Name.ToUpperInvariant())" }
                $kubernetesPolicy = Join-Path $cacheRoot "tmp\$($zone.Name)-kubernetes-api-policy.yaml"
                Write-P005V122KubernetesAPIPolicy -Kubectl $kubectl -Zone $zone -OutputPath $kubernetesPolicy
                & $kubectl apply -f $kubernetesPolicy | Out-Null
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_KUBERNETES_API_POLICY_$($zone.Name.ToUpperInvariant())" }
                $externalCIDRs = Get-P005V122ExternalEgressCIDRs -Zone $zone.Name
                $externalPolicy = Join-Path $cacheRoot "tmp\$($zone.Name)-external-egress-policy.yaml"
                if (Write-P005V122ExternalEgressPolicy -Zone $zone -CIDRs $externalCIDRs -OutputPath $externalPolicy) {
                    & $kubectl apply -f $externalPolicy | Out-Null
                    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_EXTERNAL_EGRESS_POLICY_$($zone.Name.ToUpperInvariant())" }
                }

                $previousRevision = Get-P005V122HelmReleaseRevision -Helm $helm -Zone $zone
                $runtimeValues = Get-P005V122RuntimeValues -Zone $zone.Name
                $installValues = Join-Path $cacheRoot "tmp\$($zone.Name)-values.install.yaml"
                $installOverlay = Join-Path $cacheRoot "tmp\$($zone.Name)-values.install-overlay.yaml"
                try {
                    Write-P005V122ResolvedValues -BaseValuesPath $zone.BaseValues -OutputPath $installValues
                    $runtimeValues | Set-Content -LiteralPath $installOverlay -Encoding utf8
                    & $helm upgrade --install --atomic --wait $zone.Release $chartCache --namespace $zone.Namespace --create-namespace --values $installValues --values $installOverlay
                    if ($LASTEXITCODE -ne 0) { throw "BLOCKED_HELM_INSTALL_$($zone.Name.ToUpperInvariant())" }
                    if ($null -eq $previousRevision) {
                        $transaction.created_releases += $zone
                    } else {
                        $transaction.updated_releases += [ordered]@{ zone = $zone; previous_revision = $previousRevision }
                    }
                } finally {
                    Remove-Item -LiteralPath $installValues, $installOverlay -Force -ErrorAction SilentlyContinue
                }
            }

            foreach ($name in @('managers.agentteams.io', 'workers.agentteams.io', 'teams.agentteams.io', 'humans.agentteams.io')) {
                & $kubectl wait --for=condition=Established "crd/$name" --timeout=120s
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_CRD_$name" }
            }
            foreach ($zone in $zones) {
                & $kubectl rollout status "deployment/$($zone.Release)-controller" --namespace $zone.Namespace --timeout=300s
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_CONTROLLER_$($zone.Name.ToUpperInvariant())" }
                & $kubectl wait --for=create "manager/default" --namespace $zone.Namespace --timeout=300s
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_MANAGER_CREATE_$($zone.Name.ToUpperInvariant())" }
                & $kubectl wait --for="jsonpath={.status.phase}=Running" "manager/default" --namespace $zone.Namespace --timeout=300s
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_MANAGER_$($zone.Name.ToUpperInvariant())" }
                $providerPrefix = "HAOWORK_P005_$($zone.Name.ToUpperInvariant())"
                $providerBaseURL = [string](Get-Item -Path "Env:${providerPrefix}_LLM_BASE_URL" -ErrorAction SilentlyContinue).Value
                Set-P005V122HigressProviderHostHeader -Kubectl $kubectl -Zone $zone -ProviderBaseURL $providerBaseURL -CacheRoot $cacheRoot
                $podsEvidence = & $kubectl get pods --namespace $zone.Namespace -o wide
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_EVIDENCE_PODS_$($zone.Name.ToUpperInvariant())" }
                $podsEvidence | Set-Content -LiteralPath (Join-Path $evidenceRoot "$($zone.Name)-pods.txt") -Encoding utf8
                $managerEvidence = & $kubectl get managers.agentteams.io default --namespace $zone.Namespace -o yaml
                if ($LASTEXITCODE -ne 0) { throw "BLOCKED_EVIDENCE_MANAGER_$($zone.Name.ToUpperInvariant())" }
                $managerEvidence | Set-Content -LiteralPath (Join-Path $evidenceRoot "$($zone.Name)-manager.yaml") -Encoding utf8
            }

            Start-P005V122BrowserPortForward -Kubectl $kubectl -CacheRoot $cacheRoot -ClusterName $ClusterName
            & $preflightPath -Mode LocalDualNamespace -RequireBrowserEndpoint -ClusterName $ClusterName -SkipDeploymentLock
        } catch {
            $failure = $_.Exception.Message
            try {
                Invoke-P005V122Rollback -Transaction $transaction -Helm $helm -Kubectl $kubectl -Kind $kind -CacheRoot $cacheRoot -ClusterName $ClusterName
            } catch {
                throw $_
            }
            throw "BLOCKED_PARTIAL_DEPLOYMENT:$failure"
        }
    }
}
