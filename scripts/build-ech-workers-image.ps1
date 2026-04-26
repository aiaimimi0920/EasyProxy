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

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$echWorkers = Get-EasyProxyConfigSection -Config $config -Name 'echWorkers'
$context = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $echWorkers -Name 'context' -Default '.')
$dockerfile = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $echWorkers -Name 'dockerfile' -Default 'deploy/upstreams/ech-workers/Dockerfile')
if ([string]::IsNullOrWhiteSpace($Image)) {
    $Image = [string](Get-EasyProxyConfigValue -Object $echWorkers -Name 'image' -Default 'easyproxy/ech-workers-monorepo:local')
}

$args = @("build", "-f", $dockerfile, "-t", $Image)
if ($NoCache) {
    $args += "--no-cache"
}
$args += $context

Write-Host "Building ech-workers image: $Image" -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments $args -FailureMessage "ech-workers Docker build failed"

if ($Push) {
    Write-Host "Pushing ech-workers image: $Image" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments @("push", $Image) -FailureMessage "ech-workers Docker push failed"
}

Write-Host "ech-workers image ready: $Image"
