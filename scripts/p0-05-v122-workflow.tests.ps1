[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$workflowPath = Join-Path $repoRoot '.github\workflows\agentteams-v122.yml'

function Assert-True {
    param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)

    if (-not $Condition) { throw $Message }
}

if (-not (Test-Path -LiteralPath $workflowPath -PathType Leaf)) {
    throw 'BLOCKED_AGENTTEAMS_V122_WORKFLOW_MISSING'
}

$text = Get-Content -LiteralPath $workflowPath -Raw -Encoding utf8
$contractStart = $text.IndexOf("  contract:", [StringComparison]::Ordinal)
$realStart = $text.IndexOf("  real-agentteams-e2e:", [StringComparison]::Ordinal)
Assert-True ($contractStart -ge 0 -and $realStart -gt $contractStart) 'workflow must define contract before real-agentteams-e2e'
$contractText = $text.Substring($contractStart, $realStart - $contractStart)
$realText = $text.Substring($realStart)

foreach ($trigger in @('pull_request:', 'push:', 'workflow_dispatch:')) {
    Assert-True ($text.Contains($trigger)) "workflow trigger is missing: $trigger"
}
Assert-True ($text -match '(?ms)^permissions:\s*\r?\n\s+contents:\s*read\s*$') 'workflow permissions must be contents: read'
Assert-True ($contractText.Contains('windows-latest') -and $contractText.Contains('ubuntu-latest')) 'contract job must run on Windows and Ubuntu'
Assert-True ($contractText.Contains('runner.os == ''Windows''')) 'Windows-only contract tests must have an explicit runner gate'
Assert-True (-not $contractText.Contains('secrets.')) 'automatic contract job must not read GitHub Secrets'
Assert-True (-not $text.Contains('secrets.')) 'workflow must keep real runtime values on the self-hosted runner'

foreach ($script in @(
    'p0-05-v122-workflow.tests.ps1',
    'p0-05-v122-common.tests.ps1',
    'p0-05-v122-env.tests.ps1',
    'p0-05-v122-cluster-test.tests.ps1',
    'p0-05-v122-higress.tests.ps1',
    'p0-05-v122-network.tests.ps1',
    'p0-05-v122-process.tests.ps1',
    'p0-05-v122-provider.tests.ps1',
    'p0-05-v122-offline.tests.ps1'
)) {
    Assert-True ($contractText.Contains($script)) "contract job omits $script"
}
foreach ($forbidden in @('p0-05-v122-preflight.ps1', 'p0-05-v122-up.ps1', 'p0-05-v122-cluster-test.ps1')) {
    Assert-True (-not $contractText.Contains("scripts/$forbidden")) "automatic contract job must not execute $forbidden"
}

Assert-True ($realText.Contains("if: `${{ github.event_name == 'workflow_dispatch' && inputs.run_real_e2e }}")) 'real E2E must require workflow_dispatch and explicit input'
Assert-True ($realText.Contains('environment: agentteams-e2e')) 'real E2E must use the protected agentteams-e2e environment'
foreach ($label in @('self-hosted', 'windows', 'x64', 'haowork-agentteams-v122')) {
    Assert-True ($realText.Contains($label)) "real E2E runner label is missing: $label"
}
foreach ($command in @('p0-05-v122-preflight.ps1', 'p0-05-v122-up.ps1', 'p0-05-v122-cluster-test.ps1', 'p0-05-v122-down.ps1')) {
    Assert-True ($realText.Contains($command)) "real E2E omits $command"
}
Assert-True ($realText.Contains('if: always()')) 'real E2E cleanup must always run'
Assert-True ($realText.Contains('-DeleteCluster')) 'real E2E cleanup must delete its owned Kind cluster'
Assert-True ($realText.Contains('849182af8e017168a5a200a87b1062142caf462d')) 'real E2E must pin the official AgentTeams v1.2.2 commit'
Assert-True ($realText.Contains('HAOWORK_AGENTTEAMS_ENV_FILE')) 'real E2E must require a runner-local environment file'
Assert-True ($realText.Contains('Import-P005V122LocalEnvironment')) 'real E2E must use the strict local environment loader'
Assert-True ($realText.Contains('::add-mask::')) 'real E2E must mask local runtime values before invoking scripts'
Assert-True (-not $realText.Contains('GITHUB_ENV')) 'real E2E must not persist local runtime values through GITHUB_ENV'
Assert-True ($realText.Contains('HAOWORK_E2E_CLUSTER_NAME: ${{ inputs.cluster_name }}')) 'cluster name must enter PowerShell through a non-secret environment variable'
Assert-True (-not $realText.Contains("-ClusterName '`${{ inputs.cluster_name }}'")) 'cluster name must not be interpolated into PowerShell source'

foreach ($name in @(
    'HAOWORK_P005_PUBLIC_LLM_PROVIDER', 'HAOWORK_P005_PUBLIC_LLM_BASE_URL',
    'HAOWORK_P005_PUBLIC_LLM_API_KEY', 'HAOWORK_P005_PUBLIC_LLM_MODEL',
    'HAOWORK_P005_PUBLIC_EGRESS_CIDRS', 'HAOWORK_P005_INTERNAL_LLM_PROVIDER',
    'HAOWORK_P005_INTERNAL_LLM_BASE_URL', 'HAOWORK_P005_INTERNAL_LLM_API_KEY',
    'HAOWORK_P005_INTERNAL_LLM_MODEL', 'HAOWORK_P005_INTERNAL_EGRESS_CIDRS'
)) {
    Assert-True ($realText.Contains("'$name'")) "runner-local field validation is missing: $name"
}

Assert-True ($text -notmatch 'actions/upload-artifact') 'workflow must not upload raw AgentTeams artifacts'
Assert-True ($text -notmatch '(?m)(?:^|\s)(?:\.\\|scripts/)p0-05-(?:up|down|test|preflight)\.ps1(?:\s|$)') 'workflow references an obsolete v1.1.2 entrypoint'
foreach ($use in [regex]::Matches($text, '(?m)^\s*-?\s*uses:\s*[^@\s]+@([^\s#]+)')) {
    Assert-True ($use.Groups[1].Value -match '^[0-9a-f]{40}$') "action is not pinned to a full commit: $($use.Value.Trim())"
}

Write-Output 'AgentTeams v1.2.2 public workflow contract checks passed.'
