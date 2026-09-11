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

function Join-HostCIDRs {
    param([Parameter(Mandatory)][string[]]$Addresses)

    return @($Addresses | ForEach-Object {
        $address = [System.Net.IPAddress]::Parse($_)
        $prefixLength = if ($address.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork) { 32 } else { 128 }
        "$address/$prefixLength"
    }) -join ','
}

function Get-AncestorAst {
    param(
        [Parameter(Mandatory)][System.Management.Automation.Language.Ast]$Ast,
        [Parameter(Mandatory)][Type]$Type
    )

    $candidate = $Ast.Parent
    while ($null -ne $candidate -and -not $Type.IsInstanceOfType($candidate)) {
        $candidate = $candidate.Parent
    }
    return $candidate
}

function Assert-RunPreflightEgressWiring {
    param([Parameter(Mandatory)][string]$PreflightPath)

    $tokens = $null
    $parseErrors = $null
    $scriptAst = [System.Management.Automation.Language.Parser]::ParseFile($PreflightPath, [ref]$tokens, [ref]$parseErrors)
    Assert-True (@($parseErrors).Count -eq 0) 'preflight must parse before its egress wiring can be verified'
    $runAssignments = @($scriptAst.FindAll({
        param($ast)
        $ast -is [System.Management.Automation.Language.AssignmentStatementAst] -and
            $ast.Left -is [System.Management.Automation.Language.VariableExpressionAst] -and
            $ast.Left.VariablePath.UserPath -ceq 'runPreflight'
    }, $true))
    Assert-True ($runAssignments.Count -eq 1) 'preflight must define exactly one runPreflight script block'
    $runPreflightAst = $runAssignments[0].Right

    $coverageCommands = @($runPreflightAst.FindAll({
        param($ast)
        $ast -is [System.Management.Automation.Language.CommandAst] -and
            $ast.GetCommandName() -ceq 'Get-P005V122EgressCoverageState'
    }, $true))
    Assert-True ($coverageCommands.Count -eq 1) 'runPreflight must call the egress coverage function exactly once in its zone loop'
    $coverageCommand = $coverageCommands[0]
    Assert-True (
        $coverageCommand.CommandElements.Count -eq 3 -and
        $coverageCommand.CommandElements[1] -is [System.Management.Automation.Language.CommandParameterAst] -and
        $coverageCommand.CommandElements[1].ParameterName -ceq 'Zone' -and
        $coverageCommand.CommandElements[2] -is [System.Management.Automation.Language.VariableExpressionAst] -and
        $coverageCommand.CommandElements[2].VariablePath.UserPath -ceq 'zone'
    ) 'runPreflight must pass the current zone to the egress coverage function'

    $zoneLoop = Get-AncestorAst -Ast $coverageCommand -Type ([System.Management.Automation.Language.ForEachStatementAst])
    Assert-True ($null -ne $zoneLoop -and $zoneLoop.Variable.VariablePath.UserPath -ceq 'zone') 'egress coverage must run inside the zone loop'
    $zoneNames = @($zoneLoop.Condition.FindAll({
        param($ast)
        $ast -is [System.Management.Automation.Language.StringConstantExpressionAst] -and
            $ast.Value -in @('public', 'internal')
    }, $true) | ForEach-Object { $_.Value } | Select-Object -Unique)
    Assert-True ($zoneNames.Count -eq 2) 'egress coverage zone loop must include public and internal'

    foreach ($mapping in @(
        @{ Property = 'model_covered'; Blocker = 'BLOCKED_EGRESS_CIDRS' },
        @{ Property = 'infrastructure_covered'; Blocker = 'BLOCKED_INFRASTRUCTURE_EGRESS_CIDRS' }
    )) {
        $propertyReferences = @($runPreflightAst.FindAll({
            param($ast)
            $ast -is [System.Management.Automation.Language.MemberExpressionAst] -and
                $ast.Member -is [System.Management.Automation.Language.StringConstantExpressionAst] -and
                $ast.Member.Value -ceq $mapping.Property
        }, $true))
        Assert-True ($propertyReferences.Count -eq 1) "runPreflight must inspect $($mapping.Property) exactly once"
        $condition = Get-AncestorAst -Ast $propertyReferences[0] -Type ([System.Management.Automation.Language.IfStatementAst])
        Assert-True ($null -ne $condition) "$($mapping.Property) must control a blocker branch"
        $blockers = @($condition.FindAll({
            param($ast)
            $ast -is [System.Management.Automation.Language.StringConstantExpressionAst] -and
                $ast.Value -ceq $mapping.Blocker
        }, $true))
        Assert-True ($blockers.Count -eq 1) "$($mapping.Property) must map to $($mapping.Blocker)"
    }
}

$fixtureID = [guid]::NewGuid().ToString('N')
$fixturePath = Join-Path $tmpRoot "p0-05-v122-egress-functions-$fixtureID.ps1"
$previousEnvironment = @{}
$environmentNames = @(
    'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
    'HAOWORK_P005_PUBLIC_EGRESS_CIDRS',
    'HAOWORK_P005_INTERNAL_LLM_BASE_URL',
    'HAOWORK_P005_INTERNAL_EGRESS_CIDRS'
)

try {
    $preflightPath = Join-Path $PSScriptRoot 'p0-05-v122-preflight.ps1'
    $preflightText = Get-Content -LiteralPath $preflightPath -Raw -Encoding utf8
    Assert-RunPreflightEgressWiring -PreflightPath $preflightPath
    $functionStart = $preflightText.IndexOf('function Test-P005V122IPAddressInCIDR', [StringComparison]::Ordinal)
    $functionEnd = if ($functionStart -ge 0) {
        $preflightText.IndexOf('function Test-P005V122RuntimeProviderEndpoint', $functionStart, [StringComparison]::Ordinal)
    } else {
        -1
    }
    Assert-True ($functionStart -ge 0 -and $functionEnd -gt $functionStart) 'egress coverage functions must remain independently testable'
    $preflightText.Substring($functionStart, $functionEnd - $functionStart) | Set-Content -LiteralPath $fixturePath -Encoding utf8
    . $fixturePath

    foreach ($name in $environmentNames) {
        $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    }

    $dnsFixtures = @{
        'public-provider.example.test' = @('192.0.2.10', '2001:db8:10::10')
        'internal-provider.example.test' = @('198.51.100.20', '2001:db8:20::20')
        'higress-registry.cn-hangzhou.cr.aliyuncs.com' = @('192.0.2.31', '2001:db8:31::31')
        'dockerauth.cn-hangzhou.aliyuncs.com' = @('192.0.2.32', '2001:db8:32::32')
        'cri-r0xfyoxudmtseqq7-registry.oss-cn-hangzhou.aliyuncs.com' = @('192.0.2.33', '2001:db8:33::33')
    }
    $resolver = {
        param([Parameter(Mandatory)][string]$HostName)

        if (-not $dnsFixtures.ContainsKey($HostName)) { throw "unexpected DNS host: $HostName" }
        return @($dnsFixtures[$HostName] | ForEach-Object { [System.Net.IPAddress]::Parse($_) })
    }.GetNewClosure()

    $infrastructureHosts = @(Get-P005V122HigressInfrastructureHosts)
    Assert-True ($infrastructureHosts.Count -eq 3) 'preflight must validate all three official Higress infrastructure hosts'
    foreach ($expectedHost in @(
        'higress-registry.cn-hangzhou.cr.aliyuncs.com',
        'dockerauth.cn-hangzhou.aliyuncs.com',
        'cri-r0xfyoxudmtseqq7-registry.oss-cn-hangzhou.aliyuncs.com'
    )) {
        Assert-True ($infrastructureHosts -ccontains $expectedHost) "missing Higress infrastructure host: $expectedHost"
    }

    $infrastructureAddresses = @($infrastructureHosts | ForEach-Object { $dnsFixtures[$_] })
    $publicExactCIDRs = Join-HostCIDRs -Addresses @($dnsFixtures['public-provider.example.test'] + $infrastructureAddresses)
    $internalExactCIDRs = Join-HostCIDRs -Addresses @($dnsFixtures['internal-provider.example.test'] + $infrastructureAddresses)
    $env:HAOWORK_P005_PUBLIC_LLM_BASE_URL = 'https://public-provider.example.test/v1'
    $env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS = $publicExactCIDRs
    $env:HAOWORK_P005_INTERNAL_LLM_BASE_URL = 'https://internal-provider.example.test/v1'
    $env:HAOWORK_P005_INTERNAL_EGRESS_CIDRS = $internalExactCIDRs

    foreach ($zone in @('public', 'internal')) {
        $coverage = Get-P005V122EgressCoverageState -Zone $zone -AddressResolver $resolver
        Assert-True ([bool]$coverage.model_covered) "$zone model host IPv4 and IPv6 addresses must be covered"
        Assert-True ([bool]$coverage.infrastructure_covered) "$zone Higress infrastructure IPv4 and IPv6 addresses must be covered"
    }

    $env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS = '0.0.0.0/1,128.0.0.0/1,::/1,8000::/1'
    $widePrefixes = Get-P005V122EgressCoverageState -Zone public -AddressResolver $resolver
    Assert-True (-not [bool]$widePrefixes.model_covered) 'wide IPv4 and IPv6 prefixes must not satisfy explicit model host coverage'
    Assert-True (-not [bool]$widePrefixes.infrastructure_covered) 'wide IPv4 and IPv6 prefixes must not satisfy explicit infrastructure host coverage'

    $env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS = $publicExactCIDRs
    $publicCIDRs = @($env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS -split ',')
    $env:HAOWORK_P005_PUBLIC_EGRESS_CIDRS = @($publicCIDRs | Where-Object { $_ -cne '2001:db8:33::33/128' }) -join ','
    $missingInfrastructure = Get-P005V122EgressCoverageState -Zone public -AddressResolver $resolver
    Assert-True ([bool]$missingInfrastructure.model_covered) 'missing infrastructure coverage must not obscure complete model coverage'
    Assert-True (-not [bool]$missingInfrastructure.infrastructure_covered) 'one uncovered infrastructure IPv6 address must block infrastructure egress'

    $env:HAOWORK_P005_INTERNAL_EGRESS_CIDRS = Join-HostCIDRs -Addresses $infrastructureAddresses
    $missingModel = Get-P005V122EgressCoverageState -Zone internal -AddressResolver $resolver
    Assert-True (-not [bool]$missingModel.model_covered) 'uncovered model IPv4 and IPv6 addresses must block model egress'
    Assert-True ([bool]$missingModel.infrastructure_covered) 'model coverage failure must not obscure complete infrastructure coverage'

    Assert-True ($preflightText -match 'BLOCKED_INFRASTRUCTURE_EGRESS_CIDRS') 'preflight must expose missing Higress infrastructure coverage explicitly'
    Write-Output 'AgentTeams v1.2.2 egress coverage tests passed.'
} finally {
    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
    }
    Remove-Item -LiteralPath $fixturePath -Force -ErrorAction SilentlyContinue
}
