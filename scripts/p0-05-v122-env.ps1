function Import-P005V122LocalEnvironment {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory, ParameterSetName = 'Path')][string]$Path,
        [Parameter(Mandatory, ParameterSetName = 'Lines')][AllowEmptyCollection()][AllowEmptyString()][string[]]$Lines,
        [switch]$OverrideExisting,
        [switch]$RequireComplete
    )

    if ($PSCmdlet.ParameterSetName -eq 'Path') {
        if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
        $sourceLines = @(Get-Content -LiteralPath $Path -Encoding utf8)
    } else {
        $sourceLines = @($Lines)
    }

    $allowedNames = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($zone in @('PUBLIC', 'INTERNAL')) {
        foreach ($suffix in @('LLM_PROVIDER', 'LLM_BASE_URL', 'LLM_API_KEY', 'LLM_MODEL', 'EGRESS_CIDRS')) {
            [void]$allowedNames.Add("HAOWORK_P005_${zone}_${suffix}")
        }
    }
    $seenNames = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    $parsedValues = @{}

    $lineNumber = 0
    foreach ($rawLine in $sourceLines) {
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

        $parsedValues[$name] = $value
    }

    if ($RequireComplete) {
        foreach ($requiredName in $allowedNames) {
            if (-not $seenNames.Contains($requiredName)) { throw "BLOCKED_LOCAL_ENV_MISSING_KEY: $requiredName" }
        }
    }
    foreach ($name in $parsedValues.Keys) {
        if ($OverrideExisting -or $null -eq [Environment]::GetEnvironmentVariable($name, 'Process')) {
            [Environment]::SetEnvironmentVariable($name, [string]$parsedValues[$name], 'Process')
        }
    }
}

function Test-P005V122PrivateWindowsAcl {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][Security.Principal.SecurityIdentifier]$CurrentSid
    )

    try {
        $acl = Get-Acl -LiteralPath $Path -ErrorAction Stop
        $ownerSid = $acl.GetOwner([Security.Principal.SecurityIdentifier])
        $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    } catch {
        return $false
    }
    if (-not $acl.AreAccessRulesProtected -or $ownerSid.Value -cne $CurrentSid.Value) { return $false }
    $allowedSids = @($CurrentSid.Value, 'S-1-5-18', 'S-1-5-32-544')
    foreach ($rule in $rules) {
        if ($rule.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and $rule.IdentityReference.Value -notin $allowedSids) { return $false }
    }
    return $true
}

function Read-P005V122RunnerLocalEnvironmentFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][AllowNull()][AllowEmptyString()][string]$Path,
        [Parameter(Mandatory)][string]$WorkspaceRoot,
        [Parameter(Mandatory)][string]$RunnerRoot
    )

    if ($env:OS -ne 'Windows_NT') { throw 'BLOCKED_RUNNER_LOCAL_ENV_PLATFORM' }
    if ([string]::IsNullOrWhiteSpace($Path)) { throw 'BLOCKED_RUNNER_LOCAL_ENV_FILE' }
    try { $resolvedPath = [IO.Path]::GetFullPath($Path) } catch { throw 'BLOCKED_RUNNER_LOCAL_ENV_FILE' }
    if ([IO.Path]::GetPathRoot($resolvedPath) -match '^[Cc]:\\') { throw 'BLOCKED_C_DRIVE_ENV_FILE' }

    $workspacePrefix = [IO.Path]::GetFullPath($WorkspaceRoot).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if ($resolvedPath.StartsWith($workspacePrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'BLOCKED_RUNNER_LOCAL_ENV_WORKSPACE' }
    $resolvedRunnerRoot = [IO.Path]::GetFullPath($RunnerRoot).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $resolvedSecretsRoot = [IO.Path]::GetFullPath((Join-Path $resolvedRunnerRoot 'secrets')).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    if ((Split-Path -Parent $resolvedPath) -cne $resolvedSecretsRoot) { throw 'BLOCKED_RUNNER_LOCAL_ENV_LOCATION' }

    $stream = $null
    try {
        $stream = [IO.File]::Open($resolvedPath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
        $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        foreach ($protectedPath in @($resolvedRunnerRoot, $resolvedSecretsRoot, $resolvedPath)) {
            if (([IO.File]::GetAttributes($protectedPath) -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'BLOCKED_RUNNER_LOCAL_ENV_REPARSE_POINT' }
            if (-not (Test-P005V122PrivateWindowsAcl -Path $protectedPath -CurrentSid $currentSid)) { throw 'BLOCKED_RUNNER_LOCAL_ENV_ACL' }
        }
        $reader = New-Object IO.StreamReader($stream, (New-Object Text.UTF8Encoding($false, $true)), $true, 4096, $true)
        try {
            $lines = New-Object 'System.Collections.Generic.List[string]'
            while (-not $reader.EndOfStream) { [void]$lines.Add($reader.ReadLine()) }
            return @($lines)
        } finally {
            $reader.Dispose()
        }
    } catch {
        if ($_.Exception.Message -match '^BLOCKED_RUNNER_LOCAL_ENV_') { throw }
        throw 'BLOCKED_RUNNER_LOCAL_ENV_FILE'
    } finally {
        if ($null -ne $stream) { $stream.Dispose() }
    }
}
