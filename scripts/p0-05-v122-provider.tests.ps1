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
        [Parameter(Mandatory)][int[]]$Statuses
    )

    $fixtureID = [guid]::NewGuid().ToString('N')
    $serverPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.ps1"
    $readyPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.ready"
    $stdoutPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.stdout.log"
    $stderrPath = Join-Path $tmpRoot "p0-05-v122-provider-server-$fixtureID.stderr.log"
    @'
param(
    [Parameter(Mandatory)][int]$Port,
    [Parameter(Mandatory)][string]$StatusList,
    [Parameter(Mandatory)][string]$ReadyPath
)

$ErrorActionPreference = 'Stop'
$listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, $Port)
$listener.Start()
Set-Content -LiteralPath $ReadyPath -Value 'ready' -Encoding ascii
try {
    foreach ($statusText in ($StatusList -split ',')) {
        $client = $listener.AcceptTcpClient()
        try {
            $stream = $client.GetStream()
            $reader = New-Object IO.StreamReader($stream, [Text.Encoding]::ASCII, $false, 1024, $true)
            while ($true) {
                $line = $reader.ReadLine()
                if ($null -eq $line -or $line.Length -eq 0) { break }
            }
            $status = [int]$statusText
            $reason = if ($status -ge 200 -and $status -lt 300) { 'OK' } else { 'Service Unavailable' }
            $body = [Text.Encoding]::UTF8.GetBytes('{}')
            $headers = [Text.Encoding]::ASCII.GetBytes("HTTP/1.1 $status $reason`r`nContent-Type: application/json`r`nContent-Length: $($body.Length)`r`nConnection: close`r`n`r`n")
            $stream.Write($headers, 0, $headers.Length)
            $stream.Write($body, 0, $body.Length)
            $stream.Flush()
        } finally {
            $client.Close()
        }
    }
} finally {
    $listener.Stop()
}
'@ | Set-Content -LiteralPath $serverPath -Encoding utf8

    $process = Start-Process -FilePath 'powershell.exe' -ArgumentList @(
        '-NoProfile',
        '-ExecutionPolicy', 'Bypass',
        '-File', $serverPath,
        '-Port', $Port,
        '-StatusList', ($Statuses -join ','),
        '-ReadyPath', $readyPath
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
        Paths = @($serverPath, $readyPath, $stdoutPath, $stderrPath)
    }
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

$preflightText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-preflight.ps1') -Raw -Encoding utf8
$functionStart = $preflightText.IndexOf('function Test-P005V122CIDR', [StringComparison]::Ordinal)
$functionEnd = $preflightText.IndexOf('function Get-P005V122BrowserEndpoints', $functionStart, [StringComparison]::Ordinal)
Assert-True ($functionStart -ge 0 -and $functionEnd -gt $functionStart) 'provider functions must remain independently testable'
$fixturePath = Join-Path $tmpRoot ("p0-05-v122-provider-functions-$([guid]::NewGuid().ToString('N')).ps1")
$preflightText.Substring($functionStart, $functionEnd - $functionStart) | Set-Content -LiteralPath $fixturePath -Encoding utf8
. $fixturePath

$names = @(
    'HAOWORK_P005_PUBLIC_LLM_API_KEY',
    'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
    'HAOWORK_P005_PUBLIC_EGRESS_CIDRS'
)
$original = @{}
foreach ($name in $names) {
    $item = Get-Item -Path "Env:$name" -ErrorAction SilentlyContinue
    $original[$name] = if ($null -eq $item) { $null } else { [string]$item.Value }
}

try {
    $env:HAOWORK_P005_PUBLIC_LLM_API_KEY = 'test-only-provider-key'
    $env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS = '127.0.0.1/32'

    $transientPort = Get-FreeTCPPort
    $transientProvider = Start-TestProvider -Port $transientPort -Statuses @(503, 200)
    try {
        $env:HAOWORK_P005_PUBLIC_LLM_BASE_URL = "http://127.0.0.1:$transientPort/v1"
        Assert-True (Test-P005V122RuntimeProviderEndpoint -Zone public -TimeoutMilliseconds 2000) 'read-only provider preflight must retry a transient 503'
    } finally {
        Stop-TestProvider $transientProvider
    }

    $persistentPort = Get-FreeTCPPort
    $persistentProvider = Start-TestProvider -Port $persistentPort -Statuses @(503, 503, 503)
    try {
        $env:HAOWORK_P005_PUBLIC_LLM_BASE_URL = "http://127.0.0.1:$persistentPort/v1"
        Assert-True (-not (Test-P005V122RuntimeProviderEndpoint -Zone public -TimeoutMilliseconds 2000)) 'persistent provider failures must remain blocked'
    } finally {
        Stop-TestProvider $persistentProvider
    }

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
