[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Public', 'Internal')][string]$Side,
    [Parameter(Mandatory)][string[]]$LocalEndpoint,
    [Parameter(Mandatory)][string[]]$OppositeEndpoint,
    [Parameter(Mandatory)][string]$EvidencePath,
    [int]$TimeoutMilliseconds = 3000
)

$ErrorActionPreference = 'Stop'

function Test-HaoworkTcpEndpoint {
    param([string]$Endpoint, [int]$TimeoutMilliseconds)
    $parts = $Endpoint.Split('=', 2)
    if ($parts.Count -ne 2 -or [string]::IsNullOrWhiteSpace($parts[0])) { throw "endpoint must use name=host:port: $Endpoint" }
    $hostPort = $parts[1].Split(':', 2)
    $port = 0
    if ($hostPort.Count -ne 2 -or -not [int]::TryParse($hostPort[1], [ref]$port) -or $port -lt 1 -or $port -gt 65535) { throw "endpoint must use name=host:port: $Endpoint" }
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $task = $client.ConnectAsync($hostPort[0], $port)
        $connected = $false
        try {
            $connected = $task.Wait($TimeoutMilliseconds) -and $client.Connected
        } catch [System.AggregateException] {
            $connected = $false
        }
        return [PSCustomObject]@{ name = $parts[0]; endpoint = $parts[1]; connected = $connected }
    } finally {
        $client.Dispose()
    }
}

$local = @($LocalEndpoint | ForEach-Object { Test-HaoworkTcpEndpoint -Endpoint $_ -TimeoutMilliseconds $TimeoutMilliseconds })
$opposite = @($OppositeEndpoint | ForEach-Object { Test-HaoworkTcpEndpoint -Endpoint $_ -TimeoutMilliseconds $TimeoutMilliseconds })
if (@($local | Where-Object { -not $_.connected }).Count -gt 0) { throw 'one or more local zone endpoints are unreachable' }
if (@($opposite | Where-Object { $_.connected }).Count -gt 0) { throw 'one or more opposite zone endpoints are reachable' }
$evidence = [PSCustomObject]@{ schema_version = 1; side = $Side.ToLowerInvariant(); captured_at = [DateTime]::UtcNow.ToString('o'); local = $local; opposite = $opposite; firewall_modified = $false }
$directory = Split-Path -Parent $EvidencePath
New-Item -ItemType Directory -Force -Path $directory | Out-Null
$evidence | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $EvidencePath -Encoding UTF8 -NoNewline
$evidence | ConvertTo-Json -Depth 5
