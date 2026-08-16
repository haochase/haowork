function Test-P005CIDR {
  param([Parameter(Mandatory)][string]$Value)
  $parts = $Value -split '/', 2
  if ($parts.Count -ne 2 -or [string]::IsNullOrWhiteSpace($parts[0]) -or $parts[1] -notmatch '^(0|[1-9]\d*)$') { return $false }
  $address = $parts[0]
  $ip = $null
  if (-not [System.Net.IPAddress]::TryParse($address, [ref]$ip)) { return $false }
  if ($ip.ToString() -cne $address) { return $false }
  $prefix = [int]$parts[1]
  $bytes = $ip.GetAddressBytes()
  $maxPrefix = $bytes.Length * 8
  if ($prefix -le 0 -or $prefix -gt $maxPrefix) { return $false }
  if ($ip.Equals([System.Net.IPAddress]::IPv4Any) -or $ip.Equals([System.Net.IPAddress]::IPv6Any)) { return $false }
  if ($bytes.Length -eq 4 -and $bytes[0] -ge 224 -and $bytes[0] -le 239) { return $false }
  if ($bytes.Length -eq 16 -and $bytes[0] -eq 255) { return $false }

  $fullBytes = [math]::Floor($prefix / 8)
  $remaining = $prefix % 8
  if ($remaining -gt 0) {
    $mask = (0xff -shl (8 - $remaining)) -band 0xff
    $hostMask = (-bnot $mask) -band 0xff
    if (($bytes[$fullBytes] -band $hostMask) -ne 0) { return $false }
    $fullBytes++
  }
  for ($index = $fullBytes; $index -lt $bytes.Length; $index++) {
    if ($bytes[$index] -ne 0) { return $false }
  }
  return $true
}
