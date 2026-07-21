param(
  [string]$ManagementUrl = "http://127.0.0.1:29888",
  [string]$Password = ""
)

$ErrorActionPreference = "Stop"
$headers = @{}
if ($Password) { $headers.Authorization = $Password }
$uri = ($ManagementUrl.TrimEnd('/')) + "/api/gateway/status"
$status = Invoke-RestMethod -Uri $uri -Headers $headers -Method Get
$status | ConvertTo-Json -Depth 8

if ($status.enabled -and -not $status.applied) {
  throw "transparent gateway is enabled but not applied: $($status.last_error)"
}
Write-Host "Gateway status check passed. This script is read-only and does not alter host routes."
