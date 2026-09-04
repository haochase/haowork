$ErrorActionPreference = 'Stop'

$worktreeRoot = Split-Path -Parent $PSScriptRoot
$commonGitDir = (& git -C $worktreeRoot rev-parse --git-common-dir).Trim()
if ([IO.Path]::IsPathRooted($commonGitDir)) {
  $resolvedCommonGitDir = [IO.Path]::GetFullPath($commonGitDir)
} else {
  $resolvedCommonGitDir = [IO.Path]::GetFullPath((Join-Path $worktreeRoot $commonGitDir))
}
$repoRoot = Split-Path -Parent $resolvedCommonGitDir
$cacheRoot = Join-Path $repoRoot '.haowork\cache'
$upstreamRoot = Join-Path $cacheRoot 'upstream\AgentTeams-v1.2.2'
$contractPath = Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\upstream.lock.json'
New-Item -ItemType Directory -Force (Join-Path $cacheRoot 'tmp') | Out-Null

. (Join-Path $PSScriptRoot 'p0-05-v122-common.ps1')

$contract = Get-P005V122OfficialContract -ContractPath $contractPath
Assert-P005V122OfficialContract -Contract $contract
$managerImage = Get-P005V122LockedImageReference -Contract $contract -Name 'manager'
if ($managerImage -cne 'higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-manager:v1.2.2@sha256:dd11878943e4a425ff38dcc152c9d44ea0e68d97bac89f711207134b8636c0fb') {
  throw "locked Manager image reference is invalid: $managerImage"
}

if (-not (Test-P005V122DeploymentImagesReady -Contract $contract -ManagerRuntime 'openclaw' -WorkerRuntime 'openclaw')) {
  throw 'the resolved OpenClaw deployment profile was rejected'
}
if (Test-P005V122DeploymentImagesReady -Contract $contract -ManagerRuntime 'openclaw' -WorkerRuntime 'openhuman') {
  throw 'the unavailable OpenHuman deployment profile was accepted'
}
foreach ($valuesPath in @(
  (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\public-values.yaml'),
  (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\internal-values.yaml')
)) {
  if (-not (Test-P005V122ValuesDeploymentImagesReady -Contract $contract -ValuesPath $valuesPath)) {
    throw "the audited values profile was rejected: $valuesPath"
  }
}
$unavailableValues = Join-Path $cacheRoot 'tmp\p0-05-v122-openhuman-values.yaml'
$publicValues = Get-Content -LiteralPath (Join-Path $worktreeRoot 'deploy\agentteams\v1.2.2\public-values.yaml') -Raw -Encoding utf8
($publicValues -replace '(?m)^(\s*defaultRuntime:\s*)["'']?openclaw["'']?\s*$', '$1"openhuman"') | Set-Content -LiteralPath $unavailableValues -Encoding utf8
try {
  if (Test-P005V122ValuesDeploymentImagesReady -Contract $contract -ValuesPath $unavailableValues) {
    throw 'values selecting unavailable OpenHuman passed the deployment image gate'
  }
} finally {
  Remove-Item -LiteralPath $unavailableValues -Force -ErrorAction SilentlyContinue
}

$duplicateManifestPath = $contract.Clone()
$duplicateManifestPath['upstream_manifest'] = $contract['upstream_manifest'].Clone()
$duplicateManifestPath['upstream_manifest']['files'] = @($contract['upstream_manifest']['files'] | ForEach-Object { $_.Clone() })
$duplicateManifestPath['upstream_manifest']['files'][1] = $duplicateManifestPath['upstream_manifest']['files'][0].Clone()
if (Test-P005V122OfficialContract -Contract $duplicateManifestPath) {
  throw 'duplicate upstream manifest path was accepted'
}

$unknownManifestPath = $contract.Clone()
$unknownManifestPath['upstream_manifest'] = $contract['upstream_manifest'].Clone()
$unknownManifestPath['upstream_manifest']['files'] = @($contract['upstream_manifest']['files'] | ForEach-Object { $_.Clone() })
$unknownManifestPath['upstream_manifest']['files'][0]['path'] = 'helm/agentteams/unknown.yaml'
if (Test-P005V122OfficialContract -Contract $unknownManifestPath) {
  throw 'unknown upstream manifest path was accepted'
}

$missingManifestPath = $contract.Clone()
$missingManifestPath['upstream_manifest'] = $contract['upstream_manifest'].Clone()
$missingManifestPath['upstream_manifest']['files'] = @($contract['upstream_manifest']['files'] | Select-Object -Skip 1 | ForEach-Object { $_.Clone() })
if (Test-P005V122OfficialContract -Contract $missingManifestPath) {
  throw 'missing upstream manifest path was accepted'
}

if (-not (Test-P005V122ExpectedUpstreamPath -UpstreamRoot $upstreamRoot)) {
  throw 'the exact official upstream path was rejected'
}
foreach ($invalidRoot in @("$upstreamRoot-sibling", (Join-Path $upstreamRoot 'nested'))) {
  if (Test-P005V122ExpectedUpstreamPath -UpstreamRoot $invalidRoot) {
    throw "non-canonical upstream path was accepted: $invalidRoot"
  }
}

$gitFixture = Join-Path $cacheRoot 'tmp\p0-05-v122-contract-git-fixture'
if (Test-Path -LiteralPath $gitFixture) { Remove-Item -LiteralPath $gitFixture -Recurse -Force }
New-Item -ItemType Directory -Force (Join-Path $gitFixture 'helm\agentteams\crds') | Out-Null
& git -C $gitFixture init --quiet
& git -C $gitFixture config user.email 'haowork-contract-test@example.invalid'
& git -C $gitFixture config user.name 'Haowork Contract Test'
$fixtureFiles = @(
  'helm\agentteams\Chart.yaml',
  'helm\agentteams\Chart.lock',
  'helm\agentteams\values.yaml',
  'helm\agentteams\crds\workers.agentteams.io.yaml'
)
foreach ($relative in $fixtureFiles) {
  Set-Content -LiteralPath (Join-Path $gitFixture $relative) -Value "base:$relative" -Encoding utf8
}
Set-Content -LiteralPath (Join-Path $gitFixture '.gitignore') -Value '*.tgz' -Encoding utf8
& git -C $gitFixture add .
& git -C $gitFixture commit --quiet -m 'fixture'
if (-not (Test-P005V122GitClean -RepositoryRoot $gitFixture)) { throw 'clean Git fixture was rejected' }
Set-Content -LiteralPath (Join-Path $gitFixture 'untracked.txt') -Value 'dirty' -Encoding utf8
if (Test-P005V122GitClean -RepositoryRoot $gitFixture) { throw 'untracked upstream change was accepted' }
Remove-Item -LiteralPath (Join-Path $gitFixture 'untracked.txt') -Force
$ignoredArchive = Join-Path $gitFixture 'helm\agentteams\charts\probe.tgz'
New-Item -ItemType Directory -Force (Split-Path -Parent $ignoredArchive) | Out-Null
Set-Content -LiteralPath $ignoredArchive -Value 'ignored archive' -Encoding utf8
if (Test-P005V122GitClean -RepositoryRoot $gitFixture) { throw 'ignored upstream archive was accepted' }
Remove-Item -LiteralPath $ignoredArchive -Force
foreach ($relative in $fixtureFiles) {
  $path = Join-Path $gitFixture $relative
  Set-Content -LiteralPath $path -Value "dirty:$relative" -Encoding utf8
  if (Test-P005V122GitClean -RepositoryRoot $gitFixture) { throw "dirty upstream file was accepted: $relative" }
  Set-Content -LiteralPath $path -Value "base:$relative" -Encoding utf8
}
Remove-Item -LiteralPath $gitFixture -Recurse -Force

if (-not (Test-P005V122OfficialSource -Contract $contract -UpstreamRoot $upstreamRoot)) {
  throw 'official AgentTeams v1.2.2 source did not match the lock contract'
}

$legacy = $contract.Clone()
$legacy['api_group'] = 'hiclaw.io'
if (Test-P005V122OfficialContract -Contract $legacy) {
  throw 'legacy HiClaw API group was accepted'
}

$placeholder = $contract.Clone()
$placeholder['image_resolution'] = $contract['image_resolution'].Clone()
$placeholder['image_resolution']['images'] = @($contract['image_resolution']['images'] | ForEach-Object {
  $copy = $_.Clone()
  $copy['tag'] = 'REPLACE_WITH_IMAGE_TAG'
  $copy
})
if (Test-P005V122OfficialContract -Contract $placeholder) {
  throw 'placeholder image tag was accepted'
}

$driftedTag = $contract.Clone()
$driftedTag['image_resolution'] = $contract['image_resolution'].Clone()
$driftedTag['image_resolution']['images'] = @($contract['image_resolution']['images'] | ForEach-Object {
  $copy = $_.Clone()
  if ($copy['name'] -ceq 'matrix-tuwunel') { $copy['tag'] = '20260217' }
  $copy
})
if (Test-P005V122OfficialContract -Contract $driftedTag) {
  throw 'audited image tag drift was accepted'
}

'P0-05 AgentTeams v1.2.2 official contract checks passed'
