[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$commonGitDir = (& git -C $worktreeRoot rev-parse --git-common-dir).Trim()
$repoRoot = Split-Path -Parent ([IO.Path]::GetFullPath($commonGitDir))
$tmpRoot = Join-Path $repoRoot '.haowork\cache\tmp'
New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null
$env:TEMP = $tmpRoot
$env:TMP = $tmpRoot

function Assert-True {
    param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)

    if (-not $Condition) { throw $Message }
}

$upPath = Join-Path $PSScriptRoot 'p0-05-v122-up.ps1'
$upText = Get-Content -LiteralPath $upPath -Raw -Encoding utf8
$functionStart = $upText.IndexOf('function ConvertTo-P005V122YamlScalar', [StringComparison]::Ordinal)
$functionEnd = $upText.IndexOf('function Get-P005V122ProviderHost', [StringComparison]::Ordinal)
Assert-True ($functionStart -ge 0 -and $functionEnd -gt $functionStart) 'upgrade credential functions are missing from up script'

$fixtureID = [guid]::NewGuid().ToString('N')
$functionPath = Join-Path $tmpRoot "p0-05-v122-upgrade-credentials-functions-$fixtureID.ps1"
$kubectlPath = Join-Path $tmpRoot "p0-05-v122-upgrade-credentials-kubectl-$fixtureID.ps1"
try {
    $upText.Substring($functionStart, $functionEnd - $functionStart) | Set-Content -LiteralPath $functionPath -Encoding utf8
    @'
param(
    [string]$Kubeconfig,
    [string]$Namespace,
    [string]$o,
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
)
$nameIndex = [Array]::IndexOf($Arguments, 'secret') + 1
if ($nameIndex -le 0 -or $nameIndex -ge $Arguments.Count) { exit 2 }
switch ($Arguments[$nameIndex]) {
    'haowork-public-agentteams-runtime-env' {
        '{"data":{"AGENTTEAMS_ADMIN_PASSWORD":"dGVzdC1hZG1pbi1wYXNzd29yZA=="}}'
    }
    'haowork-public-agentteams-minio' {
        '{"data":{"MINIO_ROOT_PASSWORD":"dGVzdC1taW5pby1wYXNzd29yZA=="}}'
    }
    default { exit 3 }
}
'@ | Set-Content -LiteralPath $kubectlPath -Encoding utf8
    . $functionPath
    $zone = @{
        Name = 'public'
        Namespace = 'haowork-public'
        Release = 'haowork-public-agentteams'
        RuntimeSecret = 'haowork-public-agentteams-runtime-env'
    }
    $credentials = Get-P005V122ExistingRuntimeCredentials -Kubectl $kubectlPath -Zone $zone
    Assert-True ($credentials.AdminPassword -ceq 'test-admin-password') 'existing Matrix administrator password was not preserved'
    Assert-True ($credentials.MinioPassword -ceq 'test-minio-password') 'existing MinIO root password was not preserved'

    $env:HAOWORK_P005_PUBLIC_LLM_PROVIDER = 'openai-compat'
    $env:HAOWORK_P005_PUBLIC_LLM_API_KEY = 'test-only-key'
    $env:HAOWORK_P005_PUBLIC_LLM_BASE_URL = 'https://provider.example.test/v1'
    $env:HAOWORK_P005_PUBLIC_LLM_MODEL = 'test-only-model'
    $values = Get-P005V122RuntimeValues -Zone public -AdminPassword $credentials.AdminPassword -MinioPassword $credentials.MinioPassword
    Assert-True ($values -match 'adminPassword:\s+"test-admin-password"') 'preserved Matrix password was not rendered'
    Assert-True ($values -match 'rootPassword:\s+"test-minio-password"') 'preserved MinIO password was not rendered'
    Write-Output 'AgentTeams v1.2.2 upgrade credential preservation tests passed.'
} finally {
    Remove-Item -LiteralPath $functionPath, $kubectlPath -Force -ErrorAction SilentlyContinue
    foreach ($name in @(
        'HAOWORK_P005_PUBLIC_LLM_PROVIDER', 'HAOWORK_P005_PUBLIC_LLM_API_KEY',
        'HAOWORK_P005_PUBLIC_LLM_BASE_URL', 'HAOWORK_P005_PUBLIC_LLM_MODEL'
    )) {
        Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
    }
}
