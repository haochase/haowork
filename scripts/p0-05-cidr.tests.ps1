$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'p0-05-cidr.ps1')
$valid = @('10.0.0.0/8', '192.168.0.0/16', '172.16.0.0/12', '2001:db8::/32')
$invalid = @('0.0.0.0/0', '::/0', '999.999.999.999/32', '10.0.0.1/24', '224.0.0.0/4', '2001:db8::1/32', '10.0.0.0/08')
foreach ($cidr in $valid) { if (-not (Test-P005CIDR $cidr)) { throw "valid CIDR was rejected: $cidr" } }
foreach ($cidr in $invalid) { if (Test-P005CIDR $cidr) { throw "unsafe or non-canonical CIDR was accepted: $cidr" } }
'P0-05 strict CIDR checks passed'
