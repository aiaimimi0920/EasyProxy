param(
    [string]$Image = "easy-proxy/easy-proxy:local",
    [switch]$NoCache,
    [switch]$Push
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$context = Resolve-EasyProxyPath -Path '.'
$dockerfile = Resolve-EasyProxyPath -Path 'deploy/service/base/Dockerfile'

$args = @("build", "-f", $dockerfile, "-t", $Image)
if ($NoCache) {
    $args += "--no-cache"
}
$args += $context

Write-Host "Building EasyProxy image: $Image" -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments $args -FailureMessage "EasyProxy Docker build failed"

if ($Push) {
    Write-Host "Pushing EasyProxy image: $Image" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments @("push", $Image) -FailureMessage "EasyProxy Docker push failed"
}

Write-Host "EasyProxy image ready: $Image"
