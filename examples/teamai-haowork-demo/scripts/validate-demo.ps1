$ErrorActionPreference = 'Stop'
& node (Join-Path $PSScriptRoot 'validate-demo.mjs')
exit $LASTEXITCODE
