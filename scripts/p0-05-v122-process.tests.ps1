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

function Assert-Equal {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Actual,
        [Parameter(Mandatory)][string]$Expected,
        [Parameter(Mandatory)][string]$Message
    )

    if ($Actual -cne $Expected) { throw "$Message; got=$Actual want=$Expected" }
}

$expected = '2026-08-15T15:16:50.0000000Z'
$utc = [DateTime]::SpecifyKind([DateTime]'2026-08-15T15:16:50', [DateTimeKind]::Utc)
$dmtf = [System.Management.ManagementDateTimeConverter]::ToDmtfDateTime($utc.ToLocalTime())

foreach ($name in @('p0-05-v122-up.ps1', 'p0-05-v122-preflight.ps1', 'p0-05-v122-down.ps1')) {
    $scriptText = Get-Content -LiteralPath (Join-Path $PSScriptRoot $name) -Raw -Encoding utf8
    $functionStart = $scriptText.IndexOf('function Get-P005V122ProcessStartTimeUtc', [StringComparison]::Ordinal)
    $functionEnd = $scriptText.IndexOf('function ', $functionStart + 9, [StringComparison]::Ordinal)
    if ($functionStart -lt 0 -or $functionEnd -le $functionStart) { throw "$name must expose the managed process time helper" }
    $fixturePath = Join-Path $tmpRoot ("p0-05-v122-process-$([guid]::NewGuid().ToString('N')).ps1")
    try {
        $scriptText.Substring($functionStart, $functionEnd - $functionStart) | Set-Content -LiteralPath $fixturePath -Encoding utf8
        . $fixturePath
        Assert-Equal (Get-P005V122ProcessStartTimeUtc -Process ([pscustomobject]@{ CreationDate = $utc })) $expected "$name must accept CIM DateTime values"
        Assert-Equal (Get-P005V122ProcessStartTimeUtc -Process ([pscustomobject]@{ CreationDate = $dmtf })) $expected "$name must retain DMTF compatibility"
    } finally {
        Remove-Item -LiteralPath $fixturePath -Force -ErrorAction SilentlyContinue
    }
}

Write-Output 'AgentTeams v1.2.2 managed process time tests passed.'
