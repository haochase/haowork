Set-StrictMode -Version Latest

function Get-HaoworkArtifactSha256 {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$ArtifactPath)

    if (-not (Test-Path -LiteralPath $ArtifactPath -PathType Leaf)) {
        throw "handoff artifact does not exist: $ArtifactPath"
    }
    return (Get-FileHash -LiteralPath $ArtifactPath -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Assert-HaoworkHandoffStage {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Stage,
        [Parameter(Mandatory)][string]$SourceEnvironmentID,
        [Parameter(Mandatory)][string]$TargetEnvironmentID
    )

    $source = $SourceEnvironmentID.Trim().ToLowerInvariant()
    $target = $TargetEnvironmentID.Trim().ToLowerInvariant()
    if ($source -eq '' -or $target -eq '' -or $source -eq $target) {
        throw 'handoff source and target environments must be distinct'
    }
    switch ($Stage) {
        'public-export' { if ($source -ne 'public' -or $target -ne 'internal') { throw 'public-export must move from public to internal' } }
        'internal-import' { if ($source -ne 'public' -or $target -ne 'internal') { throw 'internal-import must accept public to internal' } }
        'internal-return' { if ($source -ne 'internal' -or $target -ne 'public') { throw 'internal-return must move from internal to public' } }
        'public-merge' { if ($source -ne 'internal' -or $target -ne 'public') { throw 'public-merge must accept internal to public' } }
        default { throw "unsupported handoff stage: $Stage" }
    }
}

function New-HaoworkHandoffReceipt {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Stage,
        [Parameter(Mandatory)][string]$ArtifactPath,
        [Parameter(Mandatory)][string]$TransferID,
        [Parameter(Mandatory)][string]$SourceEnvironmentID,
        [Parameter(Mandatory)][string]$TargetEnvironmentID
    )

    Assert-HaoworkHandoffStage -Stage $Stage -SourceEnvironmentID $SourceEnvironmentID -TargetEnvironmentID $TargetEnvironmentID
    if ([string]::IsNullOrWhiteSpace($TransferID)) {
        throw 'transfer ID is required'
    }
    [PSCustomObject]@{
        schema_version        = 1
        stage                 = $Stage
        transfer_id           = $TransferID.Trim()
        source_environment_id = $SourceEnvironmentID.Trim().ToLowerInvariant()
        target_environment_id = $TargetEnvironmentID.Trim().ToLowerInvariant()
        artifact_name         = [System.IO.Path]::GetFileName($ArtifactPath)
        artifact_sha256       = Get-HaoworkArtifactSha256 -ArtifactPath $ArtifactPath
        recorded_at           = [DateTime]::UtcNow.ToString('o')
    }
}

function Assert-HaoworkHandoffReceipt {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]$Receipt,
        [Parameter(Mandatory)][string]$ArtifactPath
    )

    if ($Receipt.schema_version -ne 1 -or [string]::IsNullOrWhiteSpace([string]$Receipt.transfer_id)) {
        throw 'handoff receipt schema is invalid'
    }
    Assert-HaoworkHandoffStage -Stage ([string]$Receipt.stage) -SourceEnvironmentID ([string]$Receipt.source_environment_id) -TargetEnvironmentID ([string]$Receipt.target_environment_id)
    $actual = Get-HaoworkArtifactSha256 -ArtifactPath $ArtifactPath
    if ($actual -ne ([string]$Receipt.artifact_sha256).ToLowerInvariant()) {
        throw 'handoff artifact SHA-256 does not match receipt'
    }
}

function Write-HaoworkHandoffReceipt {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]$Receipt,
        [Parameter(Mandatory)][string]$ReceiptPath
    )

    $directory = Split-Path -Parent $ReceiptPath
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $temporary = "$ReceiptPath.tmp"
    $Receipt | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $temporary -Encoding UTF8 -NoNewline
    Move-Item -LiteralPath $temporary -Destination $ReceiptPath -Force
}

function Invoke-HaoworkJson {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Binary,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $output = & $Binary @Arguments '--json'
    if ($LASTEXITCODE -ne 0) {
        throw "haowork command failed with exit code $LASTEXITCODE"
    }
    try {
        return ($output | Out-String | ConvertFrom-Json -ErrorAction Stop)
    } catch {
        throw 'haowork command did not return valid JSON'
    }
}
