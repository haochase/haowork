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

function Assert-True {
    param(
        [Parameter(Mandatory)][bool]$Condition,
        [Parameter(Mandatory)][string]$Message
    )

    if (-not $Condition) { throw $Message }
}

function Import-P005V122Functions {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string[]]$Names,
        [Parameter(Mandatory)][string]$FixturePath
    )

    $tokens = $null
    $parseErrors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$parseErrors)
    Assert-True ($parseErrors.Count -eq 0) "could not parse $Path"
    foreach ($name in $Names) {
        $function = $ast.Find({
                param($node)
                $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq $name
        }, $true)
        Assert-True ($null -ne $function) "$Path must expose $name"
        $definition = $function.Extent.Text -replace '^function\s+', 'function script:'
        Invoke-Expression $definition
    }
    "# Functions imported from $Path" | Set-Content -LiteralPath $FixturePath -Encoding utf8
}

foreach ($name in @('p0-05-v122-up.ps1', 'p0-05-v122-preflight.ps1', 'p0-05-v122-down.ps1')) {
    $scriptText = Get-Content -LiteralPath (Join-Path $PSScriptRoot $name) -Raw -Encoding utf8
    Assert-True (-not $scriptText.Contains('Start-P005V122BrowserPortForward')) "$name must not start a persistent browser process"
    Assert-True (-not $scriptText.Contains('Stop-P005V122BrowserPortForward')) "$name must not stop a persistent browser process"
    Assert-True (-not $scriptText.Contains('process_start_time_utc')) "$name must not carry browser PID state"
}
Write-Output 'AgentTeams v1.2.2 process lifecycle contract checks passed.'
