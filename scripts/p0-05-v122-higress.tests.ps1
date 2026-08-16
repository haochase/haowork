$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$upPath = Join-Path $worktreeRoot 'scripts\p0-05-v122-up.ps1'
$upText = Get-Content -LiteralPath $upPath -Raw -Encoding utf8

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Get-FunctionText {
    param([string]$Name, [string]$NextName)

    $start = $upText.IndexOf("function $Name", [StringComparison]::Ordinal)
    $end = $upText.IndexOf("function $NextName", [StringComparison]::Ordinal)
    Assert-True ($start -ge 0 -and $end -gt $start) "missing inspectable function $Name"
    return $upText.Substring($start, $end - $start)
}

$pureFunctions =
    (Get-FunctionText 'Get-P005V122ProviderHost' 'Set-P005V122HigressRouteHostHeader') +
    (Get-FunctionText 'Set-P005V122HigressRouteHostHeader' 'Wait-P005V122LocalTCPPort')
Invoke-Expression $pureFunctions

Assert-True ((Get-P005V122ProviderHost -BaseURL 'https://provider.example/v1') -ceq 'provider.example') 'provider host parsing failed'
foreach ($invalid in @('http://', '/v1', 'https://user:pass@provider.example/v1')) {
    $failed = $false
    try { $null = Get-P005V122ProviderHost -BaseURL $invalid } catch { $failed = $_.Exception.Message -match 'BLOCKED_RUNTIME_PROVIDER_URL' }
    Assert-True $failed "unsafe provider URL was accepted: $invalid"
}

$route = [pscustomobject]@{
    name = 'default-ai-route'
    upstreams = @([pscustomobject]@{ provider = 'openai-compat'; weight = 100 })
    authConfig = [pscustomobject]@{ enabled = $true; allowedConsumers = @('manager') }
}
$updated = Set-P005V122HigressRouteHostHeader -Route $route -HostName 'provider.example'
Assert-True ($updated.name -ceq 'default-ai-route') 'route identity changed during host convergence'
Assert-True ($updated.upstreams[0].provider -ceq 'openai-compat') 'route upstream changed during host convergence'
Assert-True ($updated.authConfig.allowedConsumers[0] -ceq 'manager') 'route authorization changed during host convergence'
Assert-True ([bool]$updated.headerControl.enabled) 'route header control was not enabled'
Assert-True ($updated.headerControl.request.set.Count -eq 1) 'route host convergence must produce one deterministic set operation'
Assert-True ($updated.headerControl.request.set[0].key -ceq 'Host') 'route host convergence did not target Host'
Assert-True ($updated.headerControl.request.set[0].value -ceq 'provider.example') 'route host convergence used the wrong provider host'

Assert-True ($upText -match 'Set-P005V122HigressProviderHostHeader\s+-Kubectl\s+\$kubectl\s+-Zone\s+\$zone') 'real deployment must converge Higress in every zone'
Assert-True ($upText -match 'BLOCKED_HIGRESS_ROUTE_') 'Higress convergence must expose a fail-closed blocked state'

'P0-05 AgentTeams v1.2.2 Higress convergence checks passed.'
