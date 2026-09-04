[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$HaoworkBinary,
    [Parameter(Mandatory)][string]$Project,
    [Parameter(Mandatory)][string]$Request,
    [Parameter(Mandatory)][string]$Output,
    [Parameter(Mandatory)][string]$ReceiptDirectory
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'p0-05-v122-offline-common.ps1')

$requestData = Get-Content -LiteralPath $Request -Raw | ConvertFrom-Json -ErrorAction Stop
$manifest = $requestData.manifest
Assert-HaoworkHandoffStage -Stage 'public-export' -SourceEnvironmentID ([string]$manifest.SourceEnvironmentID) -TargetEnvironmentID ([string]$manifest.TargetEnvironmentID)
Invoke-HaoworkJson -Binary $HaoworkBinary -Arguments @('transfer', 'export', '--project', $Project, '--input', $Request, '--output', $Output) | Out-Null
$receipt = New-HaoworkHandoffReceipt -Stage 'public-export' -ArtifactPath $Output -TransferID ([string]$manifest.TransferID) -SourceEnvironmentID ([string]$manifest.SourceEnvironmentID) -TargetEnvironmentID ([string]$manifest.TargetEnvironmentID)
$receiptPath = Join-Path $ReceiptDirectory ($receipt.transfer_id + '-public-export.json')
Write-HaoworkHandoffReceipt -Receipt $receipt -ReceiptPath $receiptPath
$receipt | ConvertTo-Json -Depth 4
