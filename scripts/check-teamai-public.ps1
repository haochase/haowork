param([string]$CandidateDirectory)
$ErrorActionPreference = 'Stop'
try {
    if ([string]::IsNullOrWhiteSpace($CandidateDirectory) -or $args.Count -ne 0) {
        Write-Output 'TEAMAI_PUBLIC_INPUT'
        exit 1
    }
    $node = Get-Command node -CommandType Application -ErrorAction Stop | Select-Object -First 1
    & $node.Source (Join-Path $PSScriptRoot 'check-teamai-public.mjs') $CandidateDirectory
    exit $LASTEXITCODE
} catch {
    Write-Output 'TEAMAI_PUBLIC_INPUT'
    exit 1
}
