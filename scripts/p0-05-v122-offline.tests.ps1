[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$worktreeRoot = Split-Path -Parent $PSScriptRoot

function Assert-True {
    param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)
    if (-not $Condition) { throw $Message }
}

$scriptText = (Get-ChildItem -LiteralPath $PSScriptRoot -Filter 'p0-05-v122-*.ps1' -File |
    Where-Object { $_.Name -notlike '*.tests.ps1' } |
    ForEach-Object { Get-Content -LiteralPath $_.FullName -Raw -Encoding utf8 }) -join "`n"
$deployText = (Get-ChildItem -LiteralPath (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2') -File |
    ForEach-Object { Get-Content -LiteralPath $_.FullName -Raw -Encoding utf8 }) -join "`n"
$schema = Get-Content -LiteralPath (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\cluster-evidence.schema.json') -Raw -Encoding utf8 | ConvertFrom-Json

Assert-True (-not ($scriptText -match 'C:\\Users\\[^\r\n]*\\AppData\\Local\\Temp')) 'v1.2.2 scripts must not use the protected C drive Temp directory'
Assert-True (-not ($scriptText -match '(?i)GITHUB_ENV|secrets\.')) 'v1.2.2 scripts must not persist or read GitHub secrets'
Assert-True ($scriptText -match '(?i)\.haowork\\cache') 'v1.2.2 scripts must redirect generated state to the project cache'
Assert-True ($deployText -match '(?i)NOT_RUN') 'deployment documentation must explain unavailable real configuration as NOT_RUN'
Assert-True (-not ($schema | ConvertTo-Json -Depth 32 | Select-String -Pattern 'token|password|secret|authorization|credential|sender_id|room_id|s3_key|trace_id|consumer_name|route_name')) 'cluster evidence schema must not expose raw credentials or private runtime identities'
Assert-True ($schema.properties.data_path.required -contains 'assignment' -and $schema.properties.data_path.required -contains 'receipt') 'cluster evidence schema must require assignment and execution receipt evidence'

Write-Output 'AgentTeams v1.2.2 offline and redaction contract checks passed.'
