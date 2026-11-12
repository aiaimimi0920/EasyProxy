param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("easyproxy", "ech-workers")]
    [string]$Target,
    [string]$Image,
    [switch]$NoCache,
    [switch]$Push
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$settings = switch ($Target) {
    'easyproxy' { @{ Image = 'easy-proxy/easy-proxy:local'; Dockerfile = 'deploy/service/base/Dockerfile'; Label = 'EasyProxy' } }
    'ech-workers' { @{ Image = 'easyproxy/ech-workers-monorepo:local'; Dockerfile = 'deploy/upstreams/ech-workers/Dockerfile'; Label = 'ech-workers' } }
}
if (-not $PSBoundParameters.ContainsKey('Image')) { $Image = $settings.Image }
$label = $settings.Label

$context = Resolve-EasyProxyPath -Path '.'
$dockerfile = Resolve-EasyProxyPath -Path $settings.Dockerfile

$args = @("build", "-f", $dockerfile, "-t", $Image)
if ($NoCache) {
    $args += "--no-cache"
}
$args += $context

Write-Host "Building $label image: $Image" -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments $args -FailureMessage "$label Docker build failed"

if ($Push) {
    Write-Host "Pushing $label image: $Image" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments @("push", $Image) -FailureMessage "$label Docker push failed"
}

Write-Host "$label image ready: $Image"
