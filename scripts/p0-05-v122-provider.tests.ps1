[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$commonGitDir = (& git -C $worktreeRoot rev-parse --git-common-dir).Trim()
$repoRoot = Split-Path -Parent ([IO.Path]::GetFullPath($commonGitDir))
$tmpRoot = Join-Path $repoRoot '.haowork\cache\tmp'
New-Item -ItemType Directory -Force $tmpRoot | Out-Null
$env:TEMP = $tmpRoot
$env:TMP = $tmpRoot

function Assert-True {
    param(
        [Parameter(Mandatory)][bool]$Condition,
        [Parameter(Mandatory)][string]$Message
    )

    if (-not $Condition) { throw $Message }
}

function Get-FreeTCPPort {
    $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
}

function Start-TestProvider {
    param(
        [Parameter(Mandatory)][int]$Port,
        [Parameter(Mandatory)][object[]]$Responses,
        [Parameter(Mandatory)][string]$ExpectedKey,
        [Parameter(Mandatory)][string]$ExpectedModel
    )

    $fixtureID = [guid]::NewGuid().ToString('N')
    $serverPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.ps1"
    $readyPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.ready"
    $capturePath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.capture.json"
    $stdoutPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.stdout.log"
    $stderrPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.stderr.log"
    @'
param(
    [Parameter(Mandatory)][int]$Port,
    [Parameter(Mandatory)][string]$ResponsesBase64,
    [Parameter(Mandatory)][string]$ExpectedKeyBase64,
    [Parameter(Mandatory)][string]$ExpectedModelBase64,
    [Parameter(Mandatory)][string]$ReadyPath,
    [Parameter(Mandatory)][string]$CapturePath
)

$ErrorActionPreference = 'Stop'
$responsesJSON = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($ResponsesBase64))
$parsedResponses = $responsesJSON | ConvertFrom-Json
$responses = if ($parsedResponses -is [System.Array]) { $parsedResponses } else { @($parsedResponses) }
$expectedKey = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($ExpectedKeyBase64))
$expectedModel = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($ExpectedModelBase64))
$captures = @()
$listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, $Port)
$listener.Start()
Set-Content -LiteralPath $ReadyPath -Value 'ready' -Encoding ascii
try {
    foreach ($fixtureResponse in $responses) {
        $client = $listener.AcceptTcpClient()
        try {
            $stream = $client.GetStream()
            $reader = New-Object IO.StreamReader($stream, (New-Object Text.UTF8Encoding($false)), $false, 1024, $true)
            $requestLine = [string]$reader.ReadLine()
            $headers = @{}
            while ($true) {
                $line = $reader.ReadLine()
                if ($null -eq $line -or $line.Length -eq 0) { break }
                $separator = $line.IndexOf(':')
                if ($separator -gt 0) {
                    $headers[$line.Substring(0, $separator).Trim().ToLowerInvariant()] = $line.Substring($separator + 1).Trim()
                }
            }
            $contentLength = 0
            if ($headers.ContainsKey('content-length')) { $contentLength = [int]$headers['content-length'] }
            $body = ''
            if ($contentLength -gt 0) {
                $buffer = New-Object char[] $contentLength
                $read = 0
                while ($read -lt $contentLength) {
                    $count = $reader.Read($buffer, $read, $contentLength - $read)
                    if ($count -le 0) { break }
                    $read += $count
                }
                if ($read -gt 0) { $body = -join $buffer[0..($read - 1)] }
            }

            $parts = @($requestLine -split ' ')
            $method = if ($parts.Count -gt 0) { $parts[0] } else { '' }
            $path = if ($parts.Count -gt 1) { $parts[1] } else { '' }
            $bodyContractOK = $null
            $modelOK = $null
            $messagesOK = $null
            $maxTokensOK = $null
            $streamOK = $null
            if ($method -ceq 'POST') {
                $bodyContractOK = $false
                try {
                    $document = $body | ConvertFrom-Json
                    $messages = @($document.messages)
                    $modelOK = $document.model -ceq $expectedModel
                    $messagesOK = $messages.Count -eq 1 -and $messages[0].role -ceq 'user' -and $messages[0].content -ceq 'Return OK'
                    $maxTokensOK = [int]$document.max_tokens -eq 1
                    $streamOK = $document.stream -eq $false
                    $bodyContractOK = $modelOK -and $messagesOK -and $maxTokensOK -and $streamOK
                } catch {
                    $bodyContractOK = $false
                }
            }
            $captures += [ordered]@{
                method = $method
                path = $path
                authorization_ok = ($headers['authorization'] -ceq "Bearer $expectedKey")
                body_contract_ok = $bodyContractOK
                model_ok = $modelOK
                messages_ok = $messagesOK
                max_tokens_ok = $maxTokensOK
                stream_ok = $streamOK
            }
            $captures | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $CapturePath -Encoding utf8

            $delayMilliseconds = [int]$fixtureResponse.delay_milliseconds
            if ($delayMilliseconds -gt 0) { [Threading.Thread]::Sleep($delayMilliseconds) }
            $status = [int]$fixtureResponse.status
            $reason = if ($status -ge 200 -and $status -lt 300) { 'OK' } elseif ($status -eq 429) { 'Too Many Requests' } else { 'Provider Error' }
            $responseBody = [Text.Encoding]::UTF8.GetBytes([string]$fixtureResponse.body)
            $responseHeaders = [Text.Encoding]::ASCII.GetBytes("HTTP/1.1 $status $reason`r`nContent-Type: application/json`r`nContent-Length: $($responseBody.Length)`r`nConnection: close`r`n`r`n")
            try {
                $stream.Write($responseHeaders, 0, $responseHeaders.Length)
                $stream.Write($responseBody, 0, $responseBody.Length)
                $stream.Flush()
            } catch [IO.IOException] {
                # A timeout fixture expects the client to close before the delayed response.
            }
        } finally {
            $client.Close()
        }
    }
} finally {
    $listener.Stop()
}
'@ | Set-Content -LiteralPath $serverPath -Encoding utf8

    $responsesJSON = $Responses | ConvertTo-Json -Depth 4 -Compress
    $responsesBase64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($responsesJSON))
    $expectedKeyBase64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($ExpectedKey))
    $expectedModelBase64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($ExpectedModel))
    $process = Start-Process -FilePath 'powershell.exe' -ArgumentList @(
        '-NoProfile',
        '-ExecutionPolicy', 'Bypass',
        '-File', $serverPath,
        '-Port', $Port,
        '-ResponsesBase64', $responsesBase64,
        '-ExpectedKeyBase64', $expectedKeyBase64,
        '-ExpectedModelBase64', $expectedModelBase64,
        '-ReadyPath', $readyPath,
        '-CapturePath', $capturePath
    ) -WindowStyle Hidden -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru

    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    while (-not (Test-Path -LiteralPath $readyPath) -and [DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 50
        $process.Refresh()
        if ($process.HasExited) {
            $details = if (Test-Path -LiteralPath $stderrPath) { Get-Content -LiteralPath $stderrPath -Raw } else { '' }
            throw "provider fixture exited before ready: $details"
        }
    }
    if (-not (Test-Path -LiteralPath $readyPath)) { throw 'provider fixture did not become ready' }

    return [pscustomobject]@{
        Process = $process
        CapturePath = $capturePath
        StderrPath = $stderrPath
        Paths = @($serverPath, $readyPath, $capturePath, $stdoutPath, $stderrPath)
    }
}

function Get-TestProviderCapture {
    param(
        [Parameter(Mandatory)]$Fixture,
        [Parameter(Mandatory)][int]$MinimumCount
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    $captures = @()
    do {
        if (Test-Path -LiteralPath $Fixture.CapturePath -PathType Leaf) {
            $parsedCaptures = Get-Content -LiteralPath $Fixture.CapturePath -Raw -Encoding utf8 | ConvertFrom-Json
            $captures = if ($parsedCaptures -is [System.Array]) { $parsedCaptures } else { @($parsedCaptures) }
            if ($captures.Count -ge $MinimumCount) { return $captures }
        }
        $Fixture.Process.Refresh()
        if ($Fixture.Process.HasExited) {
            $fixtureError = if (Test-Path -LiteralPath $Fixture.StderrPath) { [string](Get-Content -LiteralPath $Fixture.StderrPath -Raw) } else { '' }
            throw "provider fixture exited before capturing $MinimumCount sanitized requests: $fixtureError"
        }
        Start-Sleep -Milliseconds 50
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "provider fixture captured $($captures.Count) sanitized requests; expected at least $MinimumCount"
}

function Stop-TestProvider {
    param([AllowNull()]$Fixture)

    if ($null -eq $Fixture) { return }
    if ($null -ne $Fixture.Process) {
        $Fixture.Process.Refresh()
        if (-not $Fixture.Process.HasExited) { Stop-Process -Id $Fixture.Process.Id -Force -ErrorAction SilentlyContinue }
        $Fixture.Process.Dispose()
    }
    foreach ($path in @($Fixture.Paths)) {
        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-ProviderFixtureCase {
    param(
        [Parameter(Mandatory)][object[]]$Responses,
        [Parameter(Mandatory)][bool]$ExpectedReady,
        [Parameter(Mandatory)][int]$ExpectedRequestCount,
        [int]$TimeoutMilliseconds = 2000,
        [AllowNull()][scriptblock]$RequestStreamFactory
    )

    $port = Get-FreeTCPPort
    $fixture = Start-TestProvider -Port $port -Responses $Responses -ExpectedKey $script:testKey -ExpectedModel $script:testModel
    try {
        $env:HAOWORK_P005_PUBLIC_LLM_BASE_URL = "http://127.0.0.1:$port/v1"
        Assert-True (Test-P005V122IPAddressInCIDR -IPAddress ([Net.IPAddress]::Parse('127.0.0.1')) -CIDR '127.0.0.1/32') 'provider fixture CIDR must be accepted'
        $resolvedFixtureAddresses = @([Net.Dns]::GetHostAddresses('127.0.0.1'))
        Assert-True ($resolvedFixtureAddresses.Count -gt 0) 'provider fixture address must resolve'
        Assert-True (@($resolvedFixtureAddresses | Where-Object { -not (Test-P005V122IPAddressInCIDR -IPAddress $_ -CIDR '127.0.0.1/32') }).Count -eq 0) 'all provider fixture addresses must be covered'
        $errorCountBeforeProbe = $Error.Count
        $probeParameters = @{ Zone = 'public'; TimeoutMilliseconds = $TimeoutMilliseconds }
        if ($null -ne $RequestStreamFactory) { $probeParameters.RequestStreamFactory = $RequestStreamFactory }
        $probeOutput = @(Test-P005V122RuntimeProviderEndpoint @probeParameters)
        Assert-True ($probeOutput.Count -eq 1) 'provider preflight must emit only its Boolean result'
        $captures = @(Get-TestProviderCapture -Fixture $fixture -MinimumCount 1)
        $safeCapture = $captures | ConvertTo-Json -Depth 3 -Compress
        $fixtureError = if (Test-Path -LiteralPath $fixture.StderrPath) { [string](Get-Content -LiteralPath $fixture.StderrPath -Raw) } else { '' }
        $probeErrorType = if ($Error.Count -gt $errorCountBeforeProbe) { $Error[0].Exception.GetType().FullName } else { 'none' }
        Assert-True ([bool]$probeOutput[0] -eq $ExpectedReady) "provider preflight returned the wrong readiness state: $safeCapture; probe_error_type=$probeErrorType; fixture_error=$fixtureError"
        $serializedOutput = $probeOutput | Out-String
        foreach ($sensitive in @($script:testKey, $script:testModel, 'Return OK', 'provider-private-body-marker')) {
            Assert-True ($serializedOutput -notmatch [regex]::Escape($sensitive)) 'provider preflight output retained a credential, model, request body, or provider body'
        }

        $captures = @(Get-TestProviderCapture -Fixture $fixture -MinimumCount $ExpectedRequestCount)
        Assert-True ($captures.Count -eq $ExpectedRequestCount) "provider preflight sent $($captures.Count) requests; expected $ExpectedRequestCount; $safeCapture"
        Assert-True (@($captures | Where-Object { -not $_.authorization_ok }).Count -eq 0) 'provider preflight did not authenticate every request'
        Assert-True ($captures[0].method -ceq 'GET' -and $captures[0].path -ceq '/v1/models') 'provider preflight must check GET /models first'
        foreach ($requestCapture in @($captures | Select-Object -Skip 1)) {
            if ($requestCapture.method -ceq 'GET') {
                Assert-True ($requestCapture.path -ceq '/v1/models') 'provider preflight retried the wrong discovery route'
                continue
            }
            Assert-True ($requestCapture.method -ceq 'POST' -and $requestCapture.path -ceq '/v1/chat/completions') 'provider preflight must use the OpenAI-compatible chat route'
            Assert-True ([bool]$requestCapture.body_contract_ok) "provider preflight sent the wrong minimal chat request: $safeCapture"
        }
    } finally {
        Stop-TestProvider $fixture
    }
}

$preflightText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-preflight.ps1') -Raw -Encoding utf8
$functionStart = $preflightText.IndexOf('function Test-P005V122CIDR', [StringComparison]::Ordinal)
$functionEnd = $preflightText.IndexOf('function Invoke-P005V122WithEvidenceLock', $functionStart, [StringComparison]::Ordinal)
Assert-True ($functionStart -ge 0 -and $functionEnd -gt $functionStart) 'provider functions must remain independently testable'
$fixturePath = Join-Path $tmpRoot ("p0-05-v122-provider-functions-$([guid]::NewGuid().ToString('N')).ps1")
$preflightText.Substring($functionStart, $functionEnd - $functionStart) | Set-Content -LiteralPath $fixturePath -Encoding utf8
. $fixturePath

$names = @(
    'HAOWORK_P005_PUBLIC_LLM_API_KEY',
    'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
    'HAOWORK_P005_PUBLIC_LLM_MODEL',
    'HAOWORK_P005_PUBLIC_EGRESS_CIDRS'
)
$original = @{}
foreach ($name in $names) {
    $item = Get-Item -Path "Env:$name" -ErrorAction SilentlyContinue
    $original[$name] = if ($null -eq $item) { $null } else { [string]$item.Value }
}

try {
    $script:testKey = 'test-only-provider-key'
    $script:testModel = 'test-only-provider-model'
    $env:HAOWORK_P005_PUBLIC_LLM_API_KEY = $script:testKey
    $env:HAOWORK_P005_PUBLIC_LLM_MODEL = $script:testModel
    $env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS = '127.0.0.1/32'

    Invoke-ProviderFixtureCase -ExpectedReady $true -ExpectedRequestCount 2 -Responses @(
        @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
        @{ status = 200; body = '{"choices":[{"message":{"content":""}}]}'; delay_milliseconds = 0 }
    )

    Invoke-ProviderFixtureCase -ExpectedReady $false -ExpectedRequestCount 2 -Responses @(
        @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
        @{ status = 200; body = 'not-json-provider-private-body-marker'; delay_milliseconds = 0 }
    )

    foreach ($invalidChoicesBody in @(
        '{"id":"missing-choices"}',
        '{"choices":[]}',
        '{"choices":[null]}',
        '{"choices":[1]}',
        '{"choices":[{}]}'
    )) {
        Invoke-ProviderFixtureCase -ExpectedReady $false -ExpectedRequestCount 2 -Responses @(
            @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
            @{ status = 200; body = $invalidChoicesBody; delay_milliseconds = 0 }
        )
    }

    Invoke-ProviderFixtureCase -ExpectedReady $false -ExpectedRequestCount 2 -Responses @(
        @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
        @{ status = 402; body = '{"error":"provider-private-body-marker"}'; delay_milliseconds = 0 },
        @{ status = 402; body = '{}'; delay_milliseconds = 0 },
        @{ status = 402; body = '{}'; delay_milliseconds = 0 }
    )

    Invoke-ProviderFixtureCase -ExpectedReady $true -ExpectedRequestCount 3 -Responses @(
        @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
        @{ status = 429; body = '{"error":"rate limited"}'; delay_milliseconds = 0 },
        @{ status = 200; body = '{"choices":[{"text":""}]}'; delay_milliseconds = 0 }
    )

    Invoke-ProviderFixtureCase -ExpectedReady $false -ExpectedRequestCount 4 -TimeoutMilliseconds 100 -Responses @(
        @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
        @{ status = 200; body = '{"choices":[]}'; delay_milliseconds = 300 },
        @{ status = 200; body = '{"choices":[]}'; delay_milliseconds = 300 },
        @{ status = 200; body = '{"choices":[]}'; delay_milliseconds = 300 }
    )

    Invoke-ProviderFixtureCase -ExpectedReady $true -ExpectedRequestCount 3 -Responses @(
        @{ status = 503; body = '{}'; delay_milliseconds = 0 },
        @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
        @{ status = 200; body = '{"choices":[{"message":{}}]}'; delay_milliseconds = 0 }
    )

    $script:requestStreamAttempts = 0
    $transientRequestStreamFactory = {
        param($Request)

        $script:requestStreamAttempts++
        if ($script:requestStreamAttempts -eq 1) {
            throw [Net.WebException]::new('fixture connect failure', $null, [Net.WebExceptionStatus]::ConnectFailure, $null)
        }
        if ($script:requestStreamAttempts -eq 2) {
            throw [Net.WebException]::new('fixture timeout', $null, [Net.WebExceptionStatus]::Timeout, $null)
        }
        return $Request.GetRequestStream()
    }
    Invoke-ProviderFixtureCase -ExpectedReady $true -ExpectedRequestCount 2 -RequestStreamFactory $transientRequestStreamFactory -Responses @(
        @{ status = 200; body = '{"data":[]}'; delay_milliseconds = 0 },
        @{ status = 200; body = '{"choices":[{"message":{}}]}'; delay_milliseconds = 0 }
    )
    Assert-True ($script:requestStreamAttempts -eq 3) 'provider preflight must retry request-stream connect failures and timeouts'

    Write-Output 'AgentTeams v1.2.2 provider preflight tests passed.'
} finally {
    Remove-Item -LiteralPath $fixturePath -Force -ErrorAction SilentlyContinue
    foreach ($name in $names) {
        if ($null -eq $original[$name]) {
            Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
        } else {
            Set-Item -Path "Env:$name" -Value $original[$name]
        }
    }
}
