[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$HaoworkBinary,
    [Parameter(Mandatory)][string]$Project,
    [Parameter(Mandatory)][string]$ReturnArchive,
    [Parameter(Mandatory)][string]$OwnerActor,
    [Parameter(Mandatory)][string]$ReceiptDirectory,
    [switch]$Confirm
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'p0-05-v122-offline-common.ps1')

$preview = Invoke-HaoworkJson -Binary $HaoworkBinary -Arguments @('transfer', 'preview', '--project', $Project, '--input', $ReturnArchive)
Assert-HaoworkHandoffStage -Stage 'public-merge' -SourceEnvironmentID ([string]$preview.manifest.SourceEnvironmentID) -TargetEnvironmentID ([string]$preview.manifest.TargetEnvironmentID)
if (-not $Confirm) {
    [PSCustomObject]@{ status = 'previewed'; preview_hash = $preview.preview_hash; rebind_required = $preview.rebind_required } | ConvertTo-Json -Depth 4
    return
}
Invoke-HaoworkJson -Binary $HaoworkBinary -Arguments @('transfer', 'apply', '--project', $Project, '--preview-hash', $preview.preview_hash, '--actor', $OwnerActor, '--role', 'owner', '--confirm') | Out-Null
$receipt = New-HaoworkHandoffReceipt -Stage 'public-merge' -ArtifactPath $ReturnArchive -TransferID ([string]$preview.manifest.TransferID) -SourceEnvironmentID ([string]$preview.manifest.SourceEnvironmentID) -TargetEnvironmentID ([string]$preview.manifest.TargetEnvironmentID)
$receiptPath = Join-Path $ReceiptDirectory ($receipt.transfer_id + '-public-merge.json')
Write-HaoworkHandoffReceipt -Receipt $receipt -ReceiptPath $receiptPath
$receipt | ConvertTo-Json -Depth 4
