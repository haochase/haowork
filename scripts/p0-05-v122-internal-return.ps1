[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$HaoworkBinary,
    [Parameter(Mandatory)][string]$Project,
    [Parameter(Mandatory)][string]$Request,
    [Parameter(Mandatory)][string]$Output,
    [Parameter(Mandatory)][string]$ReceiptDirectory,
    [switch]$Confirm
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'p0-05-v122-offline-common.ps1')

if (-not $Confirm) {
    throw 'internal-return requires -Confirm after the approval referenced by the request has been reviewed in Core'
}
$requestData = Get-Content -LiteralPath $Request -Raw | ConvertFrom-Json -ErrorAction Stop
$base = $requestData.Base
Assert-HaoworkHandoffStage -Stage 'internal-import' -SourceEnvironmentID ([string]$base.SourceEnvironmentID) -TargetEnvironmentID ([string]$base.TargetEnvironmentID)
$result = Invoke-HaoworkJson -Binary $HaoworkBinary -Arguments @('transfer', 'return', '--project', $Project, '--input', $Request, '--output', $Output)
$receipt = New-HaoworkHandoffReceipt -Stage 'internal-return' -ArtifactPath $Output -TransferID (([string]$base.TransferID) + '-return') -SourceEnvironmentID 'internal' -TargetEnvironmentID 'public'
$receiptPath = Join-Path $ReceiptDirectory ($receipt.transfer_id + '-internal-return.json')
Write-HaoworkHandoffReceipt -Receipt $receipt -ReceiptPath $receiptPath
[PSCustomObject]@{ receipt = $receipt; conflicts = $result.conflicts } | ConvertTo-Json -Depth 5
