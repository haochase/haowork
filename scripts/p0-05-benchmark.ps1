[CmdletBinding()]
param(
  [ValidateRange(3, 100)][int]$Repetitions = 3,
  [string]$Scenario = 'bench/p0-05/scenario.yaml',
  [string]$Output = '.haowork/cache/bench/p0-05',
  [switch]$ReportOnly
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$root = [IO.Path]::GetFullPath($root)
$env:GOCACHE = Join-Path $root '.haowork/cache/go'
$env:GOMODCACHE = Join-Path $root '.haowork/cache/gomod'
$env:TEMP = Join-Path $root '.haowork/cache/tmp'
$env:TMP = $env:TEMP
$npmCache = Join-Path $root '.haowork/cache/npm'
New-Item -ItemType Directory -Force $env:GOCACHE,$env:GOMODCACHE,$env:TEMP,$npmCache | Out-Null
$scenarioPath = Join-Path $root $Scenario
$outputPath = Join-Path $root $Output
$binary = Join-Path $root '.haowork/cache/haowork-bench.exe'
go build -o $binary ./cmd/haowork-bench
if (-not $ReportOnly) {
  if ([string]::IsNullOrWhiteSpace($env:HAOWORK_P005_BENCHMARK_COMMAND)) {
    throw 'benchmark blocked: HAOWORK_P005_BENCHMARK_COMMAND must invoke a real AgentTeams deployment; no synthetic success is allowed.'
  }
  if ([string]::IsNullOrWhiteSpace($env:HAOWORK_P005_BENCHMARK_ENDPOINT) -or [string]::IsNullOrWhiteSpace($env:HAOWORK_P005_BENCHMARK_PUBLIC_KEY)) {
    throw 'benchmark blocked: real AgentTeams endpoint and Ed25519 public key are required.'
  }
  & $binary run --scenario $scenarioPath --arms A,B,C,D --repetitions $Repetitions --output $outputPath
  if ($LASTEXITCODE -ne 0) { throw "benchmark run failed with exit code $LASTEXITCODE" }
}
& $binary report --input $outputPath --output (Join-Path $outputPath 'report.json')
if ($LASTEXITCODE -ne 0) { throw "benchmark report failed with exit code $LASTEXITCODE" }
Write-Output "Benchmark evidence written to $(Join-Path $outputPath 'report.json')"
