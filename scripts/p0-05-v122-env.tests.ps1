[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$commonGitDir = (& git -C $worktreeRoot rev-parse --git-common-dir).Trim()
$repoRoot = Split-Path -Parent ([IO.Path]::GetFullPath($commonGitDir))
$cacheRoot = Join-Path $repoRoot '.haowork\cache'
$tmpRoot = Join-Path $cacheRoot 'tmp'
$deploymentRoot = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2'
$loaderPath = Join-Path $PSScriptRoot 'p0-05-v122-env.ps1'
$allowedNames = @(
    'HAOWORK_P005_PUBLIC_LLM_PROVIDER',
    'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
    'HAOWORK_P005_PUBLIC_LLM_API_KEY',
    'HAOWORK_P005_PUBLIC_LLM_MODEL',
    'HAOWORK_P005_PUBLIC_EGRESS_CIDRS',
    'HAOWORK_P005_INTERNAL_LLM_PROVIDER',
    'HAOWORK_P005_INTERNAL_LLM_BASE_URL',
    'HAOWORK_P005_INTERNAL_LLM_API_KEY',
    'HAOWORK_P005_INTERNAL_LLM_MODEL',
    'HAOWORK_P005_INTERNAL_EGRESS_CIDRS'
)

function Assert-True {
    param(
        [Parameter(Mandatory)][bool]$Condition,
        [Parameter(Mandatory)][string]$Message
    )

    if (-not $Condition) { throw $Message }
}

function Assert-ThrowsLike {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$Pattern,
        [Parameter(Mandatory)][string]$Message
    )

    try {
        & $Action
    } catch {
        Assert-True ($_.Exception.Message -match $Pattern) "$Message; actual: $($_.Exception.Message)"
        return
    }
    throw "$Message; action did not fail"
}

function Write-TestEnvironmentFile {
    param([Parameter(Mandatory)][AllowEmptyString()][string[]]$Lines)

    $path = Join-Path $tmpRoot ("p0-05-v122-env-$([guid]::NewGuid().ToString('N')).local")
    $Lines | Set-Content -LiteralPath $path -Encoding utf8
    return $path
}

function Set-TestPrivateDirectoryAcl {
    param([Parameter(Mandatory)][string]$Path)

    $security = New-Object System.Security.AccessControl.DirectorySecurity
    $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $security.SetOwner($currentSid)
    $security.SetAccessRuleProtection($true, $false)
    $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    foreach ($sid in @($currentSid, (New-Object Security.Principal.SecurityIdentifier('S-1-5-18')), (New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))) {
        $security.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::FullControl, $inheritance, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)))
    }
    Set-Acl -LiteralPath $Path -AclObject $security
}

New-Item -ItemType Directory -Force $tmpRoot | Out-Null
$env:TEMP = $tmpRoot
$env:TMP = $tmpRoot

$original = @{}
foreach ($name in $allowedNames) {
    $item = Get-Item -Path "Env:$name" -ErrorAction SilentlyContinue
    $original[$name] = if ($null -eq $item) { $null } else { [string]$item.Value }
    Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
}

$created = @()
try {
    Assert-True (Test-Path -LiteralPath $loaderPath -PathType Leaf) "environment loader is missing: $loaderPath"
    . $loaderPath
    Assert-ThrowsLike -Action { Read-P005V122RunnerLocalEnvironmentFile -Path $null -WorkspaceRoot $worktreeRoot -RunnerRoot $repoRoot } -Pattern 'BLOCKED_RUNNER_LOCAL_ENV_FILE' -Message 'missing runner-local environment path must expose an explicit blocker'

    $validPath = Write-TestEnvironmentFile -Lines @(
        '# Public and Internal remain independent.',
        'HAOWORK_P005_PUBLIC_LLM_PROVIDER=openai-compat',
        'HAOWORK_P005_PUBLIC_LLM_BASE_URL=https://public.example.test/v1',
        'HAOWORK_P005_PUBLIC_LLM_API_KEY="test-only public key#1"',
        'HAOWORK_P005_PUBLIC_LLM_MODEL=public-model',
        'HAOWORK_P005_PUBLIC_EGRESS_CIDRS=192.0.2.10/32',
        '',
        'HAOWORK_P005_INTERNAL_LLM_PROVIDER=''openai-compat''',
        'HAOWORK_P005_INTERNAL_LLM_BASE_URL=https://internal.example.test/v1',
        'HAOWORK_P005_INTERNAL_LLM_API_KEY=test-only-internal-key',
        'HAOWORK_P005_INTERNAL_LLM_MODEL=internal-model',
        'HAOWORK_P005_INTERNAL_EGRESS_CIDRS=198.51.100.20/32'
    )
    $created += $validPath
    $env:HAOWORK_P005_PUBLIC_LLM_MODEL = 'environment-wins'
    Import-P005V122LocalEnvironment -Path $validPath

    Assert-True ($env:HAOWORK_P005_PUBLIC_LLM_PROVIDER -ceq 'openai-compat') 'public provider was not loaded'
    Assert-True ($env:HAOWORK_P005_PUBLIC_LLM_BASE_URL -ceq 'https://public.example.test/v1') 'public base URL was not loaded'
    Assert-True ($env:HAOWORK_P005_PUBLIC_LLM_API_KEY -ceq 'test-only public key#1') 'quoted public API key was not parsed literally'
    Assert-True ($env:HAOWORK_P005_PUBLIC_LLM_MODEL -ceq 'environment-wins') 'existing process environment must override .env.local'
    Assert-True ($env:HAOWORK_P005_INTERNAL_LLM_BASE_URL -ceq 'https://internal.example.test/v1') 'internal base URL was not loaded independently'
    Assert-True ($env:HAOWORK_P005_INTERNAL_LLM_MODEL -ceq 'internal-model') 'internal model was not loaded independently'

    $env:HAOWORK_P005_PUBLIC_LLM_MODEL = 'ambient-value'
    Import-P005V122LocalEnvironment -Path $validPath -OverrideExisting -RequireComplete
    Assert-True ($env:HAOWORK_P005_PUBLIC_LLM_MODEL -ceq 'public-model') 'strict runner import must replace an ambient process value'
    $env:HAOWORK_P005_PUBLIC_LLM_MODEL = 'environment-wins'

    $incompletePath = Write-TestEnvironmentFile -Lines @('HAOWORK_P005_PUBLIC_LLM_MODEL=only-one-field')
    $created += $incompletePath
    Assert-ThrowsLike -Action { Import-P005V122LocalEnvironment -Path $incompletePath -OverrideExisting -RequireComplete } -Pattern 'BLOCKED_LOCAL_ENV_MISSING_KEY' -Message 'strict runner import must reject an incomplete local file'
    Assert-True ($env:HAOWORK_P005_PUBLIC_LLM_MODEL -ceq 'environment-wins') 'a rejected strict import must not partially mutate the process environment'

    if ($env:OS -eq 'Windows_NT') {
        $runnerRoot = Join-Path $tmpRoot ("p0-05-v122-runner-$([guid]::NewGuid().ToString('N'))")
        $secretsRoot = Join-Path $runnerRoot 'secrets'
        $workspaceRoot = Join-Path $runnerRoot '_work\haowork\haowork'
        New-Item -ItemType Directory -Force $secretsRoot, $workspaceRoot | Out-Null
        $created += $runnerRoot
        Set-TestPrivateDirectoryAcl -Path $runnerRoot
        Set-TestPrivateDirectoryAcl -Path $secretsRoot
        $securePath = Join-Path $secretsRoot 'agentteams-v122.env'
        Copy-Item -LiteralPath $validPath -Destination $securePath
        $security = New-Object System.Security.AccessControl.FileSecurity
        $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $security.SetOwner($currentSid)
        $security.SetAccessRuleProtection($true, $false)
        foreach ($sid in @($currentSid, (New-Object Security.Principal.SecurityIdentifier('S-1-5-18')), (New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))) {
            $security.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::FullControl, [Security.AccessControl.AccessControlType]::Allow)))
        }
        Set-Acl -LiteralPath $securePath -AclObject $security
        $secureLines = @(Read-P005V122RunnerLocalEnvironmentFile -Path $securePath -WorkspaceRoot $workspaceRoot -RunnerRoot $runnerRoot)
        Assert-True ($secureLines.Count -ge 10) 'secure runner-local environment file was rejected or not read from its verified handle'
        Import-P005V122LocalEnvironment -Lines $secureLines -OverrideExisting -RequireComplete
        Assert-True ($env:HAOWORK_P005_PUBLIC_LLM_MODEL -ceq 'public-model') 'verified runner-local lines were not accepted by the strict parser'
        $env:HAOWORK_P005_PUBLIC_LLM_MODEL = 'environment-wins'

        $insecurePath = Join-Path $secretsRoot 'insecure.env'
        Copy-Item -LiteralPath $validPath -Destination $insecurePath
        Assert-ThrowsLike -Action { Read-P005V122RunnerLocalEnvironmentFile -Path $insecurePath -WorkspaceRoot $workspaceRoot -RunnerRoot $runnerRoot } -Pattern 'BLOCKED_RUNNER_LOCAL_ENV_ACL' -Message 'inherited runner-local environment ACL was accepted'
        Assert-ThrowsLike -Action { Read-P005V122RunnerLocalEnvironmentFile -Path $securePath -WorkspaceRoot $runnerRoot -RunnerRoot $runnerRoot } -Pattern 'BLOCKED_RUNNER_LOCAL_ENV_WORKSPACE' -Message 'runner-local environment file inside the workspace boundary was accepted'
    }

    $preflightText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-preflight.ps1') -Raw -Encoding utf8
    $upText = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'p0-05-v122-up.ps1') -Raw -Encoding utf8
    foreach ($text in @($preflightText, $upText)) {
        Assert-True ($text -match 'p0-05-v122-env\.ps1') 'deployment entry point must load the local environment parser'
        Assert-True ($text -match 'Import-P005V122LocalEnvironment') 'deployment entry point must import .env.local'
    }
    Assert-True ($preflightText -match 'HAOWORK_P005_PUBLIC_LLM_MODEL') 'preflight must require the public model ID'
    Assert-True ($preflightText -match 'HAOWORK_P005_INTERNAL_LLM_MODEL') 'preflight must require the internal model ID'

    $runtimeStart = $upText.IndexOf('function ConvertTo-P005V122YamlScalar', [StringComparison]::Ordinal)
    $runtimeEnd = $upText.IndexOf('function Get-P005V122RenderOnlyValues', $runtimeStart, [StringComparison]::Ordinal)
    Assert-True ($runtimeStart -ge 0 -and $runtimeEnd -gt $runtimeStart) 'runtime values functions must remain independently testable'
    $runtimeFixturePath = Join-Path $tmpRoot ("p0-05-v122-runtime-env-$([guid]::NewGuid().ToString('N')).ps1")
    $created += $runtimeFixturePath
    $upText.Substring($runtimeStart, $runtimeEnd - $runtimeStart) | Set-Content -LiteralPath $runtimeFixturePath -Encoding utf8
    . $runtimeFixturePath

    $publicValues = Get-P005V122RuntimeValues -Zone 'public'
    $internalValues = Get-P005V122RuntimeValues -Zone 'internal'
    Assert-True ($publicValues -match 'llmProvider:\s+"openai-compat"') 'public Helm values must include its provider'
    Assert-True ($publicValues -match 'defaultModel:\s+"environment-wins"') 'public Helm values must include the environment-overridden model'
    Assert-True ($publicValues -match 'llmBaseUrl:\s+"https://public\.example\.test/v1"') 'public Helm values must include its base URL'
    Assert-True ($publicValues -match 'llmApiKey:\s+"test-only public key#1"') 'public Helm values must include its API key without logging it'
    Assert-True ($publicValues -notmatch 'internal-model|internal\.example\.test') 'public Helm values must not contain internal configuration'
    Assert-True ($internalValues -match 'defaultModel:\s+"internal-model"') 'internal Helm values must include its model'
    Assert-True ($internalValues -match 'llmBaseUrl:\s+"https://internal\.example\.test/v1"') 'internal Helm values must include its base URL'
    Assert-True ($internalValues -notmatch 'environment-wins|public\.example\.test') 'internal Helm values must not contain public configuration'

    Remove-Item Env:HAOWORK_P005_PUBLIC_LLM_MODEL
    Assert-ThrowsLike -Action { Get-P005V122RuntimeValues -Zone 'public' } -Pattern 'BLOCKED_RUNTIME_CREDENTIALS' -Message 'a missing model ID must fail closed'
    $env:HAOWORK_P005_PUBLIC_LLM_MODEL = 'environment-wins'

    $unknownPath = Write-TestEnvironmentFile -Lines @('HAOWORK_P005_PUBLIC_UNKNOWN=value')
    $created += $unknownPath
    Assert-ThrowsLike -Action { Import-P005V122LocalEnvironment -Path $unknownPath } -Pattern 'BLOCKED_LOCAL_ENV_UNKNOWN_KEY' -Message 'unknown keys must fail closed'

    $duplicatePath = Write-TestEnvironmentFile -Lines @(
        'HAOWORK_P005_PUBLIC_LLM_MODEL=first',
        'HAOWORK_P005_PUBLIC_LLM_MODEL=second'
    )
    $created += $duplicatePath
    Assert-ThrowsLike -Action { Import-P005V122LocalEnvironment -Path $duplicatePath } -Pattern 'BLOCKED_LOCAL_ENV_DUPLICATE_KEY' -Message 'duplicate keys must fail closed'

    $malformedPath = Write-TestEnvironmentFile -Lines @('HAOWORK_P005_PUBLIC_LLM_API_KEY="unterminated')
    $created += $malformedPath
    Assert-ThrowsLike -Action { Import-P005V122LocalEnvironment -Path $malformedPath } -Pattern 'BLOCKED_LOCAL_ENV_VALUE' -Message 'unterminated quoted values must fail closed'

    & git -C $worktreeRoot check-ignore --quiet -- 'deploy/agentteams/v1.2.2/.env.local'
    Assert-True ($LASTEXITCODE -eq 0) '.env.local must be ignored by Git'
    & git -C $worktreeRoot check-ignore --quiet -- 'deploy/agentteams/v1.2.2/.env.example'
    Assert-True ($LASTEXITCODE -ne 0) '.env.example must remain trackable'

    $exampleText = Get-Content -LiteralPath (Join-Path $deploymentRoot '.env.example') -Raw -Encoding utf8
    foreach ($name in $allowedNames) {
        Assert-True ($exampleText -match "(?m)^$([regex]::Escape($name))=") ".env.example is missing $name"
    }
    Assert-True ($exampleText -notmatch 'sk-[A-Za-z0-9_-]{12,}') '.env.example must not contain a credential-like token'

    Write-Output 'AgentTeams v1.2.2 local environment tests passed.'
} finally {
    foreach ($path in $created) {
        if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Recurse -Force }
    }
    foreach ($name in $allowedNames) {
        if ($null -eq $original[$name]) {
            Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
        } else {
            Set-Item -Path "Env:$name" -Value $original[$name]
        }
    }
}
