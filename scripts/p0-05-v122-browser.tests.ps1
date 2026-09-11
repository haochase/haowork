[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path $PSScriptRoot 'p0-05-v122-browser.ps1'

function Assert-True {
    param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)
    if (-not $Condition) { throw $Message }
}

function Assert-Throws {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$Pattern,
        [Parameter(Mandatory)][string]$Message
    )
    $thrown = $false
    try { & $Action } catch {
        $thrown = $_.Exception.Message -match $Pattern
    }
    Assert-True $thrown $Message
}

Assert-True (Test-Path -LiteralPath $scriptPath -PathType Leaf) 'foreground browser script must exist'
$text = Get-Content -LiteralPath $scriptPath -Raw -Encoding utf8
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
Assert-True ($parseErrors.Count -eq 0) 'foreground browser script must parse'
Assert-True ($text -match 'function\s+Get-P005V122BrowserSpec') 'browser script must expose fixed zone spec'
Assert-True ($text -match 'port-forward') 'browser script must call kubectl port-forward'
Assert-True ($text -match '--address.{0,80}127\.0\.0\.1') 'browser port-forward must bind loopback only'
Assert-True ($text -match 'service/\$\(') 'browser port-forward must target service derived from spec'
Assert-True (-not $text.Contains('Start-Process')) 'browser script must remain foreground'
Assert-True (-not $text.Contains('browser-port-forward')) 'browser script must not create browser state or PID artifacts'
Assert-True (-not $text.Contains('process_start_time_utc')) 'browser script must not persist process identity'
Assert-True (-not $text.Contains('Get-Command')) 'browser script must not trust PATH-only executable resolution'
Assert-True (-not $text.Contains('rev-parse') -and -not $text.Contains('git -C')) 'browser script must not require git to locate its cache'
Assert-True ($text -match '--cache-dir') 'every kubectl call must bind an E-drive cache directory'
Assert-True ($text -match '--request-timeout=10s') 'every kubectl call must use a bounded request timeout'
Assert-True ($text -match 'BLOCKED_KIND_QUERY|BLOCKED_CLUSTER_MISSING|BLOCKED_CLUSTER_OWNERSHIP') 'browser script must distinguish cluster and ownership blockers'
Assert-True ($text -notmatch '(?m)^\s*Port\s*=') 'browser spec must not duplicate LocalPort as Port'

$function = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -ceq 'Get-P005V122BrowserSpec'
    }, $true)
Assert-True ($null -ne $function) 'browser spec helper must be importable'
Invoke-Expression ($function.Extent.Text -replace '^function\s+', 'function script:')

$clusterFunction = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -ceq 'Assert-P005V122ClusterName'
    }, $true)
Assert-True ($null -ne $clusterFunction) 'cluster validator must be importable'
Invoke-Expression ($clusterFunction.Extent.Text -replace '^function\s+', 'function script:')

$repoFunction = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -ceq 'Get-P005V122BrowserRepoRoot'
    }, $true)
Assert-True ($null -ne $repoFunction) 'repo-root marker helper must be importable'
Invoke-Expression ($repoFunction.Extent.Text -replace '^function\s+', 'function script:')
$repoFixture = Join-Path (Split-Path -Parent $PSScriptRoot) '.haowork\cache\tmp\p0-05-v122-repo-root-fixtures'
New-Item -ItemType Directory -Force $repoFixture | Out-Null
try {
    $ordinaryRoot = Join-Path $repoFixture 'ordinary'
    New-Item -ItemType Directory -Force (Join-Path $ordinaryRoot '.git'), (Join-Path $ordinaryRoot '.haowork') | Out-Null
    Assert-True ([IO.Path]::GetFullPath((Get-P005V122BrowserRepoRoot -WorktreeRoot $ordinaryRoot)) -ceq [IO.Path]::GetFullPath($ordinaryRoot)) 'ordinary repository .git directory must resolve to worktree root'

    $commonRoot = Join-Path $repoFixture 'common'
    $commonGit = Join-Path $commonRoot '.git'
    $worktreeRoot = Join-Path $repoFixture 'linked'
    $worktreeGitDir = Join-Path $commonGit 'worktrees\linked'
    New-Item -ItemType Directory -Force $worktreeGitDir, (Join-Path $commonRoot '.haowork'), $worktreeRoot | Out-Null
    "gitdir: $worktreeGitDir" | Set-Content -LiteralPath (Join-Path $worktreeRoot '.git') -Encoding ascii
    Assert-True ([IO.Path]::GetFullPath((Get-P005V122BrowserRepoRoot -WorktreeRoot $worktreeRoot)) -ceq [IO.Path]::GetFullPath($commonRoot)) 'absolute worktree gitdir marker must resolve to common repository root'

    $relativeRoot = Join-Path $repoFixture 'relative-linked'
    New-Item -ItemType Directory -Force $relativeRoot | Out-Null
    "gitdir: ..\common\.git\worktrees\linked" | Set-Content -LiteralPath (Join-Path $relativeRoot '.git') -Encoding ascii
    Assert-True ([IO.Path]::GetFullPath((Get-P005V122BrowserRepoRoot -WorktreeRoot $relativeRoot)) -ceq [IO.Path]::GetFullPath($commonRoot)) 'relative worktree gitdir marker must resolve to common repository root'

    $missingRoot = Join-Path $repoFixture 'missing'
    New-Item -ItemType Directory -Force $missingRoot | Out-Null
    Assert-Throws { Get-P005V122BrowserRepoRoot -WorktreeRoot $missingRoot } 'BLOCKED_GIT_WORKTREE' 'missing .git marker must fail closed'
    $malformedRoot = Join-Path $repoFixture 'malformed'
    New-Item -ItemType Directory -Force $malformedRoot | Out-Null
    'not-a-git-marker' | Set-Content -LiteralPath (Join-Path $malformedRoot '.git') -Encoding ascii
    Assert-Throws { Get-P005V122BrowserRepoRoot -WorktreeRoot $malformedRoot } 'BLOCKED_GIT_WORKTREE' 'malformed .git marker must fail closed'
    $outsideRoot = Join-Path $repoFixture 'outside'
    New-Item -ItemType Directory -Force $outsideRoot | Out-Null
    "gitdir: $outsideRoot" | Set-Content -LiteralPath (Join-Path $worktreeRoot '.git') -Encoding ascii
    Assert-Throws { Get-P005V122BrowserRepoRoot -WorktreeRoot $worktreeRoot } 'BLOCKED_GIT_WORKTREE' 'marker outside a valid common git tree must fail closed'
} finally {
    Remove-Item -LiteralPath $repoFixture -Recurse -Force -ErrorAction SilentlyContinue
}

$public = Get-P005V122BrowserSpec -Zone public
Assert-True ([string]$public.Namespace -ceq 'haowork-public') 'public namespace mapping'
Assert-True ([string]$public.ServiceName -ceq 'haowork-public-agentteams-element-web') 'public Element service mapping'
Assert-True ([int]$public.LocalPort -eq 18080 -and [int]$public.RemotePort -eq 8080) 'public port mapping'
$internal = Get-P005V122BrowserSpec -Zone internal
Assert-True ([string]$internal.Namespace -ceq 'haowork-internal') 'internal namespace mapping'
Assert-True ([string]$internal.ServiceName -ceq 'haowork-internal-agentteams-element-web') 'internal Element service mapping'
Assert-True ([int]$internal.LocalPort -eq 18082 -and [int]$internal.RemotePort -eq 8080) 'internal port mapping'

Assert-Throws { Get-P005V122BrowserSpec -Zone 'gateway' } 'BLOCKED_BROWSER_ZONE' 'invalid zone must fail closed'

$ownershipFunction = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -ceq 'Assert-P005V122BrowserClusterOwnership'
    }, $true)
Assert-True ($null -ne $ownershipFunction) 'browser ownership validator must be importable'
Invoke-Expression ($ownershipFunction.Extent.Text -replace '^function\s+', 'function script:')
$ownership = [pscustomobject]@{ schema_version = 1; cluster_name = 'browser'; cluster_identity = 'uid-1'; kube_context = 'kind-browser' }
Assert-P005V122BrowserClusterOwnership -Ownership $ownership -ClusterName browser -ClusterIdentity uid-1 -KubeContext kind-browser
Assert-Throws { Assert-P005V122BrowserClusterOwnership -Ownership $ownership -ClusterName other -ClusterIdentity uid-1 -KubeContext kind-browser } 'BLOCKED_CLUSTER_OWNERSHIP' 'ownership cluster name mismatch must fail closed'

$resolverFunction = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -ceq 'Resolve-P005V122TrustedExecutable'
    }, $true)
Assert-True ($null -ne $resolverFunction) 'trusted executable resolver must be importable'
Invoke-Expression ($resolverFunction.Extent.Text -replace '^function\s+', 'function script:')
$resolverRoot = Join-Path (Split-Path -Parent $PSScriptRoot) '.haowork\cache\tmp\p0-05-v122-browser-resolver'
try {
    New-Item -ItemType Directory -Force (Join-Path $resolverRoot '.tools\bin') | Out-Null
    $trustedKind = Join-Path $resolverRoot '.tools\bin\kind.exe'
    $trustedKubectl = Join-Path $resolverRoot '.tools\bin\kubectl.exe'
    New-Item -ItemType File -Force $trustedKind, $trustedKubectl | Out-Null
    Assert-True ((Resolve-P005V122TrustedExecutable -RepoRoot $resolverRoot -Name kind) -ceq [IO.Path]::GetFullPath($trustedKind)) 'kind resolver must use repo-local trusted executable'
    Assert-True ((Resolve-P005V122TrustedExecutable -RepoRoot $resolverRoot -Name kubectl) -ceq [IO.Path]::GetFullPath($trustedKubectl)) 'kubectl resolver must use repo-local trusted executable'
    Remove-Item -LiteralPath $trustedKubectl -Force
    Assert-Throws { Resolve-P005V122TrustedExecutable -RepoRoot $resolverRoot -Name kubectl } 'BLOCKED_KUBECTL' 'kubectl resolver must not fall back outside repo-local tools'
} finally {
    Remove-Item -LiteralPath $resolverRoot -Recurse -Force -ErrorAction SilentlyContinue
}

$commonPath = Join-Path $PSScriptRoot 'p0-05-v122-common.ps1'
$tmpRoot = Join-Path (Split-Path -Parent $PSScriptRoot) '.haowork\cache\tmp\p0-05-v122-browser-behavior'
New-Item -ItemType Directory -Force $tmpRoot | Out-Null
$fakeKind = Join-Path $tmpRoot 'fake-kind.cmd'
$fakeKubectl = Join-Path $tmpRoot 'fake-kubectl.cmd'
$argvPath = Join-Path $tmpRoot 'kubectl-argv.txt'
try {
    "@echo off`necho browser-test> `"$tmpRoot\kind-output.txt`"`necho browser-test`nexit /b 0" | Set-Content -LiteralPath $fakeKind -Encoding ascii
    "@echo off`necho %* > `"$argvPath`"`nexit /b 7" | Set-Content -LiteralPath $fakeKubectl -Encoding ascii

    $kindClustersFunction = $ast.Find({ param($node)
            $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -ceq 'Get-P005V122BrowserKindClusters'
        }, $true)
    Invoke-Expression ($kindClustersFunction.Extent.Text -replace '^function\s+', 'function script:')
    Assert-True ((Get-P005V122BrowserKindClusters -Kind $fakeKind) -contains 'browser-test') 'fake kind CLI output must be consumed by the cluster helper'

    $kubectlFunction = $ast.Find({ param($node)
            $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -ceq 'Invoke-P005V122BrowserKubectl'
        }, $true)
    Invoke-Expression ($kubectlFunction.Extent.Text -replace '^function\s+', 'function script:')
    $env:KUBECONFIG = Join-Path $tmpRoot 'browser.kubeconfig'
    $cacheDirectory = Join-Path $tmpRoot 'kubectl-cache'
    New-Item -ItemType Directory -Force $cacheDirectory | Out-Null
    $output = @(Invoke-P005V122BrowserKubectl -Kubectl $fakeKubectl -CacheDirectory $cacheDirectory -Arguments @('port-forward', '--address', '127.0.0.1', '--namespace', 'haowork-public', 'service/haowork-public-agentteams-element-web', '18080:8080'))
    Assert-True ($output.Count -eq 0 -and $LASTEXITCODE -eq 7) 'fake kubectl must preserve stdout and exit code as separate channels'
    $argv = Get-Content -LiteralPath $argvPath -Raw -Encoding ascii
    Assert-True ($argv -match '--cache-dir') 'captured kubectl argv must include cache-dir'
    Assert-True ($argv -match '--request-timeout=10s') 'captured kubectl argv must include bounded timeout'
    Assert-True ($argv -match 'port-forward --address 127\.0\.0\.1 --namespace haowork-public service/haowork-public-agentteams-element-web 18080:8080') 'captured argv must preserve exact foreground port-forward contract'

    $fakeServiceKubectl = Join-Path $tmpRoot 'fake-service-kubectl.cmd'
    $serviceLog = Join-Path $tmpRoot 'service-argv.txt'
    @("@echo off", "set args=%*", "echo %args%>>`"$serviceLog`"", "echo %args% | findstr /c:`"get endpointslices.discovery.k8s.io`" >nul", "if %errorlevel%==0 (", "  echo {`"items`": [{`"endpoints`": [{`"addresses`": [`"10.0.0.1`"], `"conditions`": {`"ready`": true}}], `"ports`": [{`"port`": 80}]}]}", "  exit /b 0", ")", "echo element-html", "exit /b 0") | Set-Content -LiteralPath $fakeServiceKubectl -Encoding ascii
    $commonFunctions = [System.Management.Automation.Language.Parser]::ParseFile($commonPath, [ref]$tokens, [ref]$parseErrors)
    foreach ($name in @('Get-P005V122ServiceProxySpec', 'Get-P005V122ServiceProxyPath', 'Test-P005V122ServiceReadyEndpoints', 'Invoke-P005V122ServiceProxyReadiness')) {
        $definition = $commonFunctions.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq $name }, $true)
        Invoke-Expression ($definition.Extent.Text -replace '^function\s+', 'function script:')
    }
    $env:KUBECONFIG = Join-Path $tmpRoot 'service.kubeconfig'
    Invoke-P005V122ServiceProxyReadiness -Kubectl $fakeServiceKubectl -Zone public -CacheDirectory $cacheDirectory -DeadlineSeconds 1
    $serviceArgs = Get-Content -LiteralPath $serviceLog -Raw -Encoding ascii
    Assert-True ($serviceArgs -match 'higress-gateway') 'gateway readiness must query the exact gateway endpoints'
    Assert-True ($serviceArgs -notmatch 'higress-gateway.*get --raw') 'gateway readiness must not probe an unreliable root Service Proxy body'
    Assert-True ($serviceArgs -match 'haowork-public-agentteams-element-web:8080/proxy') 'Element readiness must use its Service Proxy path'
} finally {
    Remove-Item -LiteralPath $tmpRoot -Recurse -Force -ErrorAction SilentlyContinue
}

$commonText = Get-Content -LiteralPath $commonPath -Raw -Encoding utf8
Assert-True ($commonText -match 'function\s+Get-P005V122ServiceProxySpec') 'common helper must expose fixed Service Proxy zone spec'
Assert-True ($commonText -match 'function\s+Invoke-P005V122ServiceProxyReadiness') 'common helper must expose bounded Service Proxy readiness'
Assert-True ($commonText -match 'get\s+--raw') 'Service Proxy readiness must use kubectl get --raw'
Assert-True ($commonText -match 'endpointslices\.discovery\.k8s\.io') 'service readiness must use non-deprecated EndpointSlice discovery'
Assert-True ($commonText -match '--request-timeout=10s') 'Service Proxy HTTP readiness must be bounded'
Assert-True ($commonText -match 'higress-gateway') 'Service Proxy spec must include the exact Higress gateway service'
Assert-True ($commonText -match 'BLOCKED_SERVICE_PROXY_') 'Service Proxy failures must be explicit and zone-scoped'
$commonAst = [System.Management.Automation.Language.Parser]::ParseFile($commonPath, [ref]$tokens, [ref]$parseErrors)
Assert-True ($parseErrors.Count -eq 0) 'common helper must parse'
$proxySpecFunction = $commonAst.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -ceq 'Get-P005V122ServiceProxySpec'
    }, $true)
Invoke-Expression ($proxySpecFunction.Extent.Text -replace '^function\s+', 'function script:')
$proxyPathFunction = $commonAst.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -ceq 'Get-P005V122ServiceProxyPath'
    }, $true)
Invoke-Expression ($proxyPathFunction.Extent.Text -replace '^function\s+', 'function script:')
$proxyPublic = Get-P005V122ServiceProxySpec -Zone public
Assert-True ([string]$proxyPublic.ElementServiceName -ceq 'haowork-public-agentteams-element-web') 'public Service Proxy Element mapping'
Assert-True ([string]$proxyPublic.GatewayServiceName -ceq 'higress-gateway' -and [int]$proxyPublic.GatewayPort -eq 80) 'public Service Proxy gateway mapping'
Assert-True ((Get-P005V122ServiceProxyPath -Namespace $proxyPublic.Namespace -ServiceName $proxyPublic.ElementServiceName -Port 8080) -ceq '/api/v1/namespaces/haowork-public/services/haowork-public-agentteams-element-web:8080/proxy/') 'public Service Proxy path'

$scriptRoot = Split-Path -Parent $PSScriptRoot
$fixtureRoot = Join-Path $scriptRoot '.haowork\cache\tmp\p0-05-v122-browser-test'
New-Item -ItemType Directory -Force $fixtureRoot | Out-Null
try {
    Assert-Throws { Assert-P005V122ClusterName -Name 'bad/name' } 'BLOCKED_KIND_CLUSTER_NAME' 'invalid cluster name must fail closed'
} finally {
    Remove-Item -LiteralPath $fixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Output 'P0-05 v1.2.2 foreground browser contract checks passed.'
