[CmdletBinding()]
param(
    [string]$TempRoot = (Join-Path $env:TEMP 'haowork-p005-v122-offline-tests')
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'p0-05-v122-offline-common.ps1')

function Assert-OfflineTest {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

Remove-Item -LiteralPath $TempRoot -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $TempRoot | Out-Null

try {
    $artifact = Join-Path $TempRoot 'capsule.zip'
    [System.IO.File]::WriteAllBytes($artifact, [byte[]](1, 2, 3, 4))
    $receipt = New-HaoworkHandoffReceipt -Stage 'public-export' -ArtifactPath $artifact -TransferID 'XFR-001' -SourceEnvironmentID 'public' -TargetEnvironmentID 'internal'
    Assert-OfflineTest ($receipt.schema_version -eq 1) 'receipt schema version is missing'
    Assert-OfflineTest ($receipt.artifact_sha256 -match '^[a-f0-9]{64}$') 'receipt SHA-256 is invalid'
    Assert-HaoworkHandoffReceipt -Receipt $receipt -ArtifactPath $artifact

    $tampered = Join-Path $TempRoot 'tampered.zip'
    [System.IO.File]::WriteAllBytes($tampered, [byte[]](4, 3, 2, 1))
    $tamperRejected = $false
    try { Assert-HaoworkHandoffReceipt -Receipt $receipt -ArtifactPath $tampered } catch { $tamperRejected = $true }
    Assert-OfflineTest $tamperRejected 'tampered artifact was accepted'

    $wrongEnvironmentRejected = $false
    try { New-HaoworkHandoffReceipt -Stage 'internal-return' -ArtifactPath $artifact -TransferID 'XFR-002' -SourceEnvironmentID 'public' -TargetEnvironmentID 'internal' | Out-Null } catch { $wrongEnvironmentRejected = $true }
    Assert-OfflineTest $wrongEnvironmentRejected 'wrong stage environment mapping was accepted'

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
        $proofPath = Join-Path $TempRoot 'network-proof.json'
        & powershell.exe -NoProfile -File (Join-Path $PSScriptRoot 'p0-05-v122-network-proof.ps1') -Side Public -LocalEndpoint "core=127.0.0.1:$port" -OppositeEndpoint 'matrix=127.0.0.1:9' -EvidencePath $proofPath | Out-Null
        Assert-OfflineTest ($LASTEXITCODE -eq 0) 'network proof rejected a reachable local and unreachable opposite endpoint'
        $proof = Get-Content -LiteralPath $proofPath -Raw | ConvertFrom-Json
        Assert-OfflineTest ($proof.firewall_modified -eq $false) 'network proof reported a firewall modification'
    } finally {
        $listener.Stop()
    }

    Write-Output 'PASS: P0-05 v1.2.2 offline handoff receipt checks'
} finally {
    Remove-Item -LiteralPath $TempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
