param(
    [ValidateSet("easyproxy", "ech-workers", "both")]
    [string]$Target = "both",
    [string]$ReleaseTag = "",
    [string]$GhcrOwner = $env:GITHUB_REPOSITORY_OWNER,
    [string]$GhcrUsername = $env:GHCR_USERNAME,
    [ValidateSet("linux/amd64", "linux/amd64,linux/arm64")]
    [string]$Platform = "linux/amd64",
    [switch]$LoadOnly,
    [switch]$NoCache
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-ghcr.ps1")

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."
Assert-EasyProxyCommand -Name "git" -Hint "Install Git first."

function New-DefaultReleaseTag {
    $shortSha = (git rev-parse --short HEAD 2>$null).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($shortSha)) {
        $shortSha = "manual"
    }

    return ("release-{0}-{1}" -f (Get-Date -Format 'yyyyMMdd-HHmmss'), $shortSha)
}

Assert-EasyProxyGhcrOwnerIsSafe -Owner $GhcrOwner -SourceDescription "GHCR owner"

if ([string]::IsNullOrWhiteSpace($ReleaseTag)) {
    $ReleaseTag = New-DefaultReleaseTag
}

$serviceImageName = 'easy-proxy-monorepo-service'
$echWorkersImageName = 'ech-workers-monorepo'
$imagePrefix = "ghcr.io/$GhcrOwner"

Write-Host "GHCR owner: $GhcrOwner" -ForegroundColor Cyan
Write-Host "Release tag: $ReleaseTag" -ForegroundColor Cyan
Write-Host "Target: $Target" -ForegroundColor Cyan

if ($Target -in @("easyproxy", "both")) {
    $scriptPath = Join-Path $PSScriptRoot '..\deploy\service\base\scripts\publish-ghcr-easy-proxy-service.ps1'
    $args = @(
        "-ExecutionPolicy", "Bypass",
        "-File", $scriptPath,
        "-ReleaseTag", $ReleaseTag,
        "-ImagePrefix", $imagePrefix,
        "-ImageName", $serviceImageName,
        "-Platform", $Platform
    )
    if (-not [string]::IsNullOrWhiteSpace($GhcrUsername)) { $args += @("-GhcrUsername", $GhcrUsername) }
    if ($LoadOnly) { $args += "-LoadOnly" }
    if ($NoCache) { $args += "-NoCache" }
    Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "GHCR publish failed for EasyProxy service image"
}

if ($Target -in @("ech-workers", "both")) {
    $scriptPath = Join-Path $PSScriptRoot '..\deploy\upstreams\ech-workers\scripts\publish-ghcr-ech-workers.ps1'
    $args = @(
        "-ExecutionPolicy", "Bypass",
        "-File", $scriptPath,
        "-ReleaseTag", $ReleaseTag,
        "-ImagePrefix", $imagePrefix,
        "-ImageName", $echWorkersImageName,
        "-Platform", $Platform
    )
    if (-not [string]::IsNullOrWhiteSpace($GhcrUsername)) { $args += @("-GhcrUsername", $GhcrUsername) }
    if ($LoadOnly) { $args += "-LoadOnly" }
    if ($NoCache) { $args += "-NoCache" }
    Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "GHCR publish failed for ech-workers image"
}

Write-Host "Done publishing target: $Target" -ForegroundColor Green
