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

$fixtureID = [guid]::NewGuid().ToString('N')
$fixturePath = Join-Path $tmpRoot "p0-05-v122-network-functions-$fixtureID.ps1"
$kubectlPath = Join-Path $tmpRoot "p0-05-v122-network-kubectl-$fixtureID.cmd"
$policyPath = Join-Path $tmpRoot "p0-05-v122-network-policy-$fixtureID.yaml"

try {
    $upText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-up.ps1') -Raw -Encoding utf8
    $functionStart = $upText.IndexOf('function Write-P005V122KubernetesAPIPolicy', [StringComparison]::Ordinal)
    $functionEnd = $upText.IndexOf('function Write-P005V122ExternalEgressPolicy', $functionStart, [StringComparison]::Ordinal)
    Assert-True ($functionStart -ge 0 -and $functionEnd -gt $functionStart) 'Kubernetes API policy function must remain independently testable'
    $upText.Substring($functionStart, $functionEnd - $functionStart) | Set-Content -LiteralPath $fixturePath -Encoding utf8
    . $fixturePath

    @'
@echo off
echo %* | findstr /c:"get service kubernetes" >nul && (
  echo 10.96.0.1
  exit /b 0
)
echo %* | findstr /c:"get endpoints kubernetes" >nul && (
  echo {"subsets":[{"addresses":[{"ip":"172.18.0.2"}],"ports":[{"port":6443,"protocol":"TCP"}]}]}
  exit /b 0
)
exit /b 1
'@ | Set-Content -LiteralPath $kubectlPath -Encoding ascii

    $zone = @{ Name = 'public'; Namespace = 'haowork-public' }
    Write-P005V122KubernetesAPIPolicy -Kubectl $kubectlPath -Zone $zone -OutputPath $policyPath
    $policy = Get-Content -LiteralPath $policyPath -Raw -Encoding utf8

    Assert-True ($policy -match '(?ms)cidr:\s*10\.96\.0\.1/32.*?port:\s*443') 'policy must allow the Kubernetes Service VIP'
    Assert-True ($policy -match '(?ms)cidr:\s*172\.18\.0\.2/32.*?port:\s*6443') 'policy must allow the Kind API endpoint after service DNAT'
    Assert-True ($policy -notmatch '0\.0\.0\.0/0|::/0') 'policy must never broaden Kubernetes API egress to all networks'

    Write-Output 'AgentTeams v1.2.2 Kubernetes API policy tests passed.'
} finally {
    foreach ($path in @($fixturePath, $kubectlPath, $policyPath)) {
        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
    }
}
