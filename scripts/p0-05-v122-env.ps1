function Import-P005V122LocalEnvironment {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }

    $allowedNames = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($zone in @('PUBLIC', 'INTERNAL')) {
        foreach ($suffix in @('LLM_PROVIDER', 'LLM_BASE_URL', 'LLM_API_KEY', 'LLM_MODEL', 'EGRESS_CIDRS')) {
            [void]$allowedNames.Add("HAOWORK_P005_${zone}_${suffix}")
        }
    }
    $seenNames = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)

    $lineNumber = 0
    foreach ($rawLine in Get-Content -LiteralPath $Path -Encoding utf8) {
        $lineNumber++
        $line = ([string]$rawLine).Trim()
        if ([string]::IsNullOrWhiteSpace($line) -or $line.StartsWith('#', [StringComparison]::Ordinal)) { continue }

        $separator = $line.IndexOf('=')
        if ($separator -le 0) { throw "BLOCKED_LOCAL_ENV_LINE: invalid assignment at line $lineNumber" }
        $name = $line.Substring(0, $separator).Trim()
        if ($name -cnotmatch '^[A-Z][A-Z0-9_]*$') { throw "BLOCKED_LOCAL_ENV_KEY: invalid key at line $lineNumber" }
        if (-not $allowedNames.Contains($name)) { throw "BLOCKED_LOCAL_ENV_UNKNOWN_KEY: $name" }
        if (-not $seenNames.Add($name)) { throw "BLOCKED_LOCAL_ENV_DUPLICATE_KEY: $name" }

        $value = $line.Substring($separator + 1).Trim()
        if ($value.Length -gt 0 -and ($value[0] -eq [char]34 -or $value[0] -eq [char]39)) {
            $quote = $value[0]
            if ($value.Length -lt 2 -or $value[$value.Length - 1] -ne $quote) {
                throw "BLOCKED_LOCAL_ENV_VALUE: unterminated quoted value at line $lineNumber"
            }
            $value = $value.Substring(1, $value.Length - 2)
        }

        if ($null -eq [Environment]::GetEnvironmentVariable($name, 'Process')) {
            [Environment]::SetEnvironmentVariable($name, $value, 'Process')
        }
    }
}
