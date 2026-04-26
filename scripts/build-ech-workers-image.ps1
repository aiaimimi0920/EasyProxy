param(
    [string]$Image = "easyproxy/ech-workers-monorepo:local",
    [switch]$NoCache,
    [switch]$Push
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$repoRoot = Get-EasyProxyRepoRoot
$dockerfile = Resolve-EasyProxyPath -Path "deploy/upstreams/ech-workers/Dockerfile"

$args = @("build", "-f", $dockerfile, "-t", $Image)
if ($NoCache) {
    $args += "--no-cache"
}
$args += $repoRoot

Write-Host "Building ech-workers image: $Image" -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments $args -FailureMessage "ech-workers Docker build failed"

if ($Push) {
    Write-Host "Pushing ech-workers image: $Image" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments @("push", $Image) -FailureMessage "ech-workers Docker push failed"
}

Write-Host "ech-workers image ready: $Image"
