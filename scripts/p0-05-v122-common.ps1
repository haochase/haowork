function Get-P005V122OfficialContract {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$ContractPath)

  if (-not (Test-Path -LiteralPath $ContractPath -PathType Leaf)) {
    throw "AgentTeams v1.2.2 contract lock does not exist: $ContractPath"
  }
  $raw = Get-Content -LiteralPath $ContractPath -Raw -Encoding utf8
  if ([string]::IsNullOrWhiteSpace($raw)) {
    throw 'AgentTeams v1.2.2 contract lock is empty'
  }
  try {
    $value = ConvertFrom-Json -InputObject $raw -ErrorAction Stop
    return ConvertTo-P005V122Hashtable -Value $value
  } catch {
    throw "AgentTeams v1.2.2 contract lock is not valid JSON: $($_.Exception.Message)"
  }
}

function ConvertTo-P005V122Hashtable {
  [CmdletBinding()]
  param([Parameter(Mandatory)][AllowNull()][object]$Value)

  if ($Value -is [System.Collections.IDictionary]) {
    $result = @{}
    foreach ($entry in $Value.GetEnumerator()) {
      $result[[string]$entry.Key] = ConvertTo-P005V122Hashtable -Value $entry.Value
    }
    return $result
  }
  if ($Value -is [System.Collections.IEnumerable] -and -not ($Value -is [string])) {
    return @($Value | ForEach-Object { ConvertTo-P005V122Hashtable -Value $_ })
  }
  if ($null -eq $Value -or $Value -is [string] -or $Value -is [ValueType]) {
    return $Value
  }
  $result = @{}
  foreach ($property in $Value.PSObject.Properties) {
    $result[$property.Name] = ConvertTo-P005V122Hashtable -Value $property.Value
  }
  return $result
}

function Test-P005V122OfficialContract {
  [CmdletBinding()]
  param([Parameter(Mandatory)][hashtable]$Contract)

  $requiredKinds = @('Human', 'Manager', 'Team', 'Worker')
  $requiredImageNames = @(
    'controller',
    'element-web',
    'manager',
    'matrix-tuwunel',
    'storage-minio',
    'worker-copaw',
    'worker-hermes',
    'worker-openclaw',
    'worker-openhuman'
  )
  $expectedImages = @{
    'matrix-tuwunel' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/tuwunel', '20260216', 'sha256:fa0f68cf591c90b12888c2df76c2ce03fb50a7cd4a9c7fe0199480b291932c00', 'RESOLVED', 'ACTIVE')
    'storage-minio' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/minio', '20260216', 'sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e', 'RESOLVED', 'ACTIVE')
    'controller' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-controller', 'v1.2.2', 'sha256:a0709506e6dd047bc6aadcfd8d77c8f193683d4326795c263f32b7be9e791570', 'RESOLVED', 'ACTIVE')
    'manager' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-manager', 'v1.2.2', 'sha256:dd11878943e4a425ff38dcc152c9d44ea0e68d97bac89f711207134b8636c0fb', 'RESOLVED', 'ACTIVE')
    'element-web' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/element-web', '20260216', 'sha256:827ae9ebea5ec0eeb487660f4f04e5789b666667f17a0d63b5c0e4ad8b9b9ca1', 'RESOLVED', 'ACTIVE')
    'worker-openclaw' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-worker', 'v1.2.2', 'sha256:301f9e311654eca203246fa666d63a126244ea8793f700603d2a6d37b7ffea75', 'RESOLVED', 'ACTIVE')
    'worker-copaw' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-copaw-worker', 'v1.2.2', 'sha256:7a6780ef76b6c7b056a2c343eeabc697f70108dae153afe8ddb76a3fad9a41b4', 'RESOLVED', 'OPTIONAL')
    'worker-hermes' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-hermes-worker', 'v1.2.2', 'sha256:e611f38e1aa2451c97b979ae944a787f0db69c9d65c21c72a05ab33b53288e4e', 'RESOLVED', 'OPTIONAL')
    'worker-openhuman' = @('higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/agentteams-openhuman-worker', 'v1.2.2', '', 'UNAVAILABLE_UPSTREAM', 'OPTIONAL')
  }
  $required = @(
    'schema_version', 'repository', 'tag', 'commit', 'chart_path', 'chart_version',
    'chart_app_version', 'upstream_manifest', 'chart_dependencies', 'api_group', 'api_version', 'kinds',
    'controller_ownership_label', 'image_resolution'
  )
  if (@($required | Where-Object { -not $Contract.ContainsKey($_) }).Count -ne 0) { return $false }
  if ($Contract['schema_version'] -ne 2 -or
      $Contract['repository'] -cne 'https://github.com/agentscope-ai/AgentTeams' -or
      $Contract['tag'] -cne 'v1.2.2' -or
      $Contract['commit'] -cne '849182af8e017168a5a200a87b1062142caf462d' -or
      $Contract['chart_path'] -cne 'helm/agentteams' -or
      $Contract['chart_version'] -cne '1.1.1' -or
      $Contract['chart_app_version'] -cne '1.1.1' -or
      $Contract['api_group'] -cne 'agentteams.io' -or
      $Contract['api_version'] -cne 'v1beta1' -or
      $Contract['controller_ownership_label'] -cne 'agentteams.io/controller') { return $false }
  $manifest = $Contract['upstream_manifest']
  $expectedManifest = @{
    'helm/agentteams/Chart.yaml' = '5c7b1b8d0968db7b452049e27e012b9668b38143b4236dea6b139e8f0467a18e'
    'helm/agentteams/Chart.lock' = 'f4ada56a4107df94d1a3175f683490c4f143c8381a66a81619aa33d42a46aa43'
    'helm/agentteams/values.yaml' = '83da031e460c3ec102ad99baf5f19b447e9b19a11ab17598b309ced5ff066e97'
    'helm/agentteams/crds/managers.agentteams.io.yaml' = '2c279e6c4203b320ffa73fb8f88a7639e5a1e8dd9a00c848579963154f7ea10a'
    'helm/agentteams/crds/workers.agentteams.io.yaml' = '3864240f99e7fa2f15e33c6886a1012fb736c871cd021e87ed2ae499234a1286'
    'helm/agentteams/crds/teams.agentteams.io.yaml' = 'bd75a92d6187d0283061d3291cf102365cf86fb2b01f8a8ddc8f4b5530fc7342'
    'helm/agentteams/crds/humans.agentteams.io.yaml' = '4637e64377856574fa4fe9bb76567fb7a926cc2bd0b1f1504afd1f71261bb897'
  }
  if ($null -eq $manifest -or $manifest['algorithm'] -cne 'sha256' -or @($manifest['files']).Count -ne $expectedManifest.Count) { return $false }
  $seenManifestPaths = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
  foreach ($entry in @($manifest['files'])) {
    $path = [string]$entry['path']
    if (-not $expectedManifest.ContainsKey($path) -or
        -not $seenManifestPaths.Add($path) -or
        $entry['sha256'] -cne $expectedManifest[$path]) { return $false }
  }
  if ($seenManifestPaths.Count -ne $expectedManifest.Count) { return $false }

  $kinds = @($Contract['kinds'] | Sort-Object)
  if ($kinds.Count -ne $requiredKinds.Count -or (@(Compare-Object -CaseSensitive $requiredKinds $kinds).Count -ne 0)) { return $false }

  $dependencies = @($Contract['chart_dependencies'])
  if ($dependencies.Count -ne 1) { return $false }
  $dependency = $dependencies[0]
  if ($dependency['name'] -cne 'higress' -or
      $dependency['repository'] -cne 'https://higress.io/helm-charts' -or
      $dependency['version'] -cne '2.2.1' -or
      $dependency['lock_digest'] -cne 'sha256:bfda3317506f04c1088d398ca7b10137409999ec54e1d36b7b5d525145ee931b') { return $false }

  $resolution = $Contract['image_resolution']
  if ($null -eq $resolution -or $resolution['status'] -cne 'RESOLVED' -or -not [string]::IsNullOrWhiteSpace([string]$resolution['reason'])) { return $false }
  if ($resolution['deployment_profile']['manager_runtime'] -cne 'openclaw' -or $resolution['deployment_profile']['worker_runtime'] -cne 'openclaw') { return $false }

  $rendered = $resolution['rendered_inventory']
  $requiredRenderedNames = @('controller', 'element-web', 'higress-console', 'higress-controller', 'higress-gateway', 'higress-pilot', 'matrix-tuwunel', 'storage-minio')
  if ($null -eq $rendered -or $rendered['status'] -cne 'RESOLVED' -or
      -not [string]::IsNullOrWhiteSpace([string]$rendered['reason']) -or
      $rendered['manifest_sha256'] -cne '4ad4b8e7e279dbe6e8687dc67daaf640870f115f53cb09ef7902953308909ffe' -or
      @($rendered['images']).Count -ne $requiredRenderedNames.Count) { return $false }
  $actualRenderedNames = @($rendered['images'] | ForEach-Object { [string]$_['name'] } | Sort-Object)
  if (@(Compare-Object -CaseSensitive $requiredRenderedNames $actualRenderedNames).Count -ne 0) { return $false }
  foreach ($image in @($rendered['images'])) {
    if ($image['resolution_status'] -cne 'RESOLVED' -or $image['requirement'] -cne 'ACTIVE' -or
        [string]$image['resolved_digest'] -notmatch '^sha256:[a-f0-9]{64}$' -or
        [string]::IsNullOrWhiteSpace([string]$image['source']) -or
        -not [string]::IsNullOrWhiteSpace([string]$image['reason'])) { return $false }
  }
  $images = @($resolution['images'])
  $actualNames = @($images | ForEach-Object { [string]$_['name'] } | Sort-Object)
  if ($actualNames.Count -ne $requiredImageNames.Count -or (@(Compare-Object -CaseSensitive $requiredImageNames $actualNames).Count -ne 0)) { return $false }
  foreach ($image in $images) {
    if ([string]::IsNullOrWhiteSpace([string]$image['name']) -or
        [string]::IsNullOrWhiteSpace([string]$image['repository']) -or
        [string]::IsNullOrWhiteSpace([string]$image['tag']) -or
        [string]::IsNullOrWhiteSpace([string]$image['source'])) { return $false }
    if ([string]$image['repository'] -match '(?i)latest|replace_with' -or [string]$image['tag'] -match '(?i)latest|replace_with') { return $false }
    $expected = $expectedImages[[string]$image['name']]
    if ($null -eq $expected -or $image['repository'] -cne $expected[0] -or $image['tag'] -cne $expected[1] -or
        [string]$image['resolved_digest'] -cne $expected[2] -or $image['resolution_status'] -cne $expected[3] -or
        $image['requirement'] -cne $expected[4]) { return $false }
    if ($image['resolution_status'] -ceq 'RESOLVED' -and ([string]$image['resolved_digest'] -notmatch '^sha256:[a-f0-9]{64}$' -or -not [string]::IsNullOrWhiteSpace([string]$image['reason']))) { return $false }
    if ($image['resolution_status'] -ceq 'UNAVAILABLE_UPSTREAM' -and ([string]$image['requirement'] -cne 'OPTIONAL' -or -not [string]::IsNullOrEmpty([string]$image['resolved_digest']) -or [string]::IsNullOrWhiteSpace([string]$image['reason']))) { return $false }
  }
  return $true
}

function Test-P005V122DeploymentImagesReady {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][hashtable]$Contract,
    [Parameter(Mandatory)][string]$ManagerRuntime,
    [Parameter(Mandatory)][string]$WorkerRuntime
  )

  if (-not (Test-P005V122OfficialContract -Contract $Contract)) { return $false }
  if ($ManagerRuntime -cne 'openclaw') { return $false }
  $requiredNames = @('controller', 'element-web', 'manager', 'matrix-tuwunel', 'storage-minio', "worker-$WorkerRuntime")
  $images = @{}
  foreach ($image in @($Contract['image_resolution']['images'])) { $images[[string]$image['name']] = $image }
  foreach ($name in $requiredNames) {
    if (-not $images.ContainsKey($name) -or $images[$name]['resolution_status'] -cne 'RESOLVED' -or [string]$images[$name]['resolved_digest'] -notmatch '^sha256:[a-f0-9]{64}$') { return $false }
  }
  return $Contract['image_resolution']['rendered_inventory']['status'] -ceq 'RESOLVED'
}

function Get-P005V122TopLevelScalar {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$ValuesPath,
    [Parameter(Mandatory)][string]$Section,
    [Parameter(Mandatory)][string]$Key
  )

  $inside = $false
  foreach ($line in @(Get-Content -LiteralPath $ValuesPath -Encoding utf8)) {
    if ($line -match '^([A-Za-z][A-Za-z0-9-]*):\s*(?:#.*)?$') {
      $inside = $Matches[1] -ceq $Section
      continue
    }
    if ($inside -and $line -match ('^  {0}:\s*["'']?([^"''#\s]+)["'']?\s*(?:#.*)?$' -f [regex]::Escape($Key))) {
      return $Matches[1]
    }
  }
  return $null
}

function Test-P005V122ValuesDeploymentImagesReady {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][hashtable]$Contract,
    [Parameter(Mandatory)][string]$ValuesPath
  )

  if (-not (Test-Path -LiteralPath $ValuesPath -PathType Leaf)) { return $false }
  $managerRuntime = Get-P005V122TopLevelScalar -ValuesPath $ValuesPath -Section 'manager' -Key 'runtime'
  $workerRuntime = Get-P005V122TopLevelScalar -ValuesPath $ValuesPath -Section 'worker' -Key 'defaultRuntime'
  if ([string]::IsNullOrWhiteSpace([string]$managerRuntime) -or [string]::IsNullOrWhiteSpace([string]$workerRuntime)) { return $false }
  return Test-P005V122DeploymentImagesReady -Contract $Contract -ManagerRuntime $managerRuntime -WorkerRuntime $workerRuntime
}

function Test-P005V122ExpectedUpstreamPath {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$UpstreamRoot)

  $repositoryRoot = Split-Path -Parent $PSScriptRoot
  $expected = Join-Path $repositoryRoot '.haowork\cache\upstream\AgentTeams-v1.2.2'
  return [IO.Path]::GetFullPath($UpstreamRoot).TrimEnd('\') -ceq [IO.Path]::GetFullPath($expected).TrimEnd('\')
}

function Test-P005V122GitClean {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$RepositoryRoot)

  $status = & git -C $RepositoryRoot status --porcelain=v1 --ignored=matching --untracked-files=all 2>$null
  return $LASTEXITCODE -eq 0 -and @($status).Count -eq 0
}

function Assert-P005V122OfficialContract {
  [CmdletBinding()]
  param([Parameter(Mandatory)][hashtable]$Contract)

  if (-not (Test-P005V122OfficialContract -Contract $Contract)) {
    throw 'AgentTeams v1.2.2 contract lock does not match the required upstream facts'
  }
}

function Test-P005V122OfficialSource {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][hashtable]$Contract,
    [Parameter(Mandatory)][string]$UpstreamRoot
  )

  if (-not (Test-P005V122OfficialContract -Contract $Contract)) { return $false }
  if (-not (Test-P005V122ExpectedUpstreamPath -UpstreamRoot $UpstreamRoot)) { return $false }
  $sourceRoot = [IO.Path]::GetFullPath($UpstreamRoot)
  if (-not (Test-Path -LiteralPath (Join-Path $sourceRoot '.git') -PathType Container)) { return $false }
  if (-not (Test-P005V122GitClean -RepositoryRoot $sourceRoot)) { return $false }

  $head = (& git -C $sourceRoot rev-parse HEAD 2>$null).Trim()
  if ($LASTEXITCODE -ne 0 -or $head -cne $Contract['commit']) { return $false }
  $tag = (& git -C $sourceRoot describe --tags --exact-match 2>$null).Trim()
  if ($LASTEXITCODE -ne 0 -or $tag -cne $Contract['tag']) { return $false }

  $chartPath = Join-Path $sourceRoot ($Contract['chart_path'] -replace '/', '\\')
  foreach ($entry in @($Contract['upstream_manifest']['files'])) {
    $target = Join-Path $sourceRoot ($entry['path'] -replace '/', '\\')
    if (-not (Test-Path -LiteralPath $target -PathType Leaf) -or (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant() -cne $entry['sha256']) { return $false }
  }
  $chart = Join-Path $chartPath 'Chart.yaml'
  if (-not (Test-Path -LiteralPath $chart -PathType Leaf)) { return $false }
  $chartText = Get-Content -LiteralPath $chart -Raw -Encoding utf8
  if ($chartText -notmatch '(?m)^version:\s+1\.1\.1\s*$' -or $chartText -notmatch '(?m)^appVersion:\s+"1\.1\.1"\s*$') { return $false }

  $expectedCRDs = @{
    'managers.agentteams.io.yaml' = 'Manager'
    'workers.agentteams.io.yaml' = 'Worker'
    'teams.agentteams.io.yaml' = 'Team'
    'humans.agentteams.io.yaml' = 'Human'
  }
  foreach ($item in $expectedCRDs.GetEnumerator()) {
    $crd = Join-Path $chartPath (Join-Path 'crds' $item.Key)
    if (-not (Test-Path -LiteralPath $crd -PathType Leaf)) { return $false }
    $crdText = Get-Content -LiteralPath $crd -Raw -Encoding utf8
    if ($crdText -notmatch '(?m)^  group:\s+agentteams\.io\s*$' -or
        $crdText -notmatch '(?m)^    - name:\s+v1beta1\s*$' -or
        $crdText -notmatch '(?m)^    kind:\s+' + [regex]::Escape($item.Value) + '\s*$') { return $false }
  }
  return $true
}
