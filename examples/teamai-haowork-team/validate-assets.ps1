$ErrorActionPreference = 'Stop'
try {
    & node (Join-Path $PSScriptRoot 'validate-assets.mjs')
    exit $LASTEXITCODE
} catch {
    [Console]::Error.WriteLine('TEAMAI_HAOWORK_ASSETS_INVALID: runtime unavailable')
    exit 1
}
