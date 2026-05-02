param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [string]$Image = "",
    [switch]$NoCache,
    [switch]$Push
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-config.ps1")

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$repoRoot = Get-EasyProxyRepoRoot
$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$serviceBase = Get-EasyProxyConfigSection -Config $config -Name 'serviceBase'
$context = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $serviceBase -Name 'context' -Default '.')
$dockerfile = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $serviceBase -Name 'dockerfile' -Default 'deploy/service/base/Dockerfile')
if ([string]::IsNullOrWhiteSpace($Image)) {
$Image = [string](Get-EasyProxyConfigValue -Object $serviceBase -Name 'image' -Default 'easy-proxy/easy-proxy:local')
}

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
