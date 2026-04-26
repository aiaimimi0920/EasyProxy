param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [ValidateSet("easyproxy", "ech-workers", "both")]
    [string]$Target = "both",
    [string]$ReleaseTag = "",
    [string]$GhcrOwner = "",
    [string]$GhcrUsername = $env:GHCR_USERNAME,
    [string]$GhcrToken = $env:GHCR_TOKEN,
    [string]$Platform = "",
    [switch]$LoadOnly,
    [switch]$NoCache
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-config.ps1")

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."
Assert-EasyProxyCommand -Name "git" -Hint "Install Git first."

function Resolve-ConfigMetadata {
    param([Parameter(Mandatory = $true)][string]$PreferredPath)

    $repoRoot = Get-EasyProxyRepoRoot
    $preferredResolved = if ([System.IO.Path]::IsPathRooted($PreferredPath)) {
        $PreferredPath
    } else {
        Join-Path $repoRoot $PreferredPath
    }

    if (Test-Path -LiteralPath $preferredResolved) {
        return [pscustomobject]@{
            Path   = (Resolve-Path -LiteralPath $preferredResolved).Path
            Exists = $true
        }
    }

    return [pscustomobject]@{
        Path   = $preferredResolved
        Exists = $false
    }
}

function Assert-GhcrOwnerIsSafe {
    param(
        [Parameter(Mandatory = $true)][string]$Owner,
        [string]$SourceDescription = "GHCR owner"
    )

    $normalized = $Owner.Trim()
    if ([string]::IsNullOrWhiteSpace($normalized)) {
        throw "$SourceDescription is empty. Set ghcr.owner in config.yaml or pass -GhcrOwner explicitly."
    }

    if ($normalized -match '^(your-github-owner|change_me.*|.*placeholder.*)$') {
        throw "$SourceDescription still uses a placeholder value: $normalized"
    }
}

function New-DefaultReleaseTag {
    $shortSha = (git rev-parse --short HEAD 2>$null).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($shortSha)) {
        $shortSha = "manual"
    }

    return ("release-{0}-{1}" -f (Get-Date -Format 'yyyyMMdd-HHmmss'), $shortSha)
}

$configMetadata = Resolve-ConfigMetadata -PreferredPath $ConfigPath
$config = [pscustomobject]@{}

if ($configMetadata.Exists) {
    $config = Read-EasyProxyConfig -ConfigPath $configMetadata.Path
}
elseif ([string]::IsNullOrWhiteSpace($GhcrOwner)) {
    throw "Config file not found: $($configMetadata.Path). Create config.yaml first or pass -GhcrOwner explicitly."
}
else {
    Write-Host "Config file not found, using built-in GHCR defaults with the explicit -GhcrOwner value." -ForegroundColor Yellow
}

$ghcr = Get-EasyProxyConfigSection -Config $config -Name 'ghcr'

if ([string]::IsNullOrWhiteSpace($GhcrOwner)) {
    $GhcrOwner = [string](Get-EasyProxyConfigValue -Object $ghcr -Name 'owner' -Default '')
}

Assert-GhcrOwnerIsSafe -Owner $GhcrOwner -SourceDescription "GHCR owner"

if ([string]::IsNullOrWhiteSpace($Platform)) {
    $Platform = [string](Get-EasyProxyConfigValue -Object $ghcr -Name 'platform' -Default 'linux/amd64')
}

if ([string]::IsNullOrWhiteSpace($ReleaseTag)) {
    $ReleaseTag = New-DefaultReleaseTag
}

$serviceImageName = [string](Get-EasyProxyConfigValue -Object $ghcr -Name 'serviceImageName' -Default 'easy-proxy-monorepo-service')
$echWorkersImageName = [string](Get-EasyProxyConfigValue -Object $ghcr -Name 'echWorkersImageName' -Default 'ech-workers-monorepo')
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
    if (-not [string]::IsNullOrWhiteSpace($GhcrToken)) { $args += @("-GhcrToken", $GhcrToken) }
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
    if (-not [string]::IsNullOrWhiteSpace($GhcrToken)) { $args += @("-GhcrToken", $GhcrToken) }
    if ($LoadOnly) { $args += "-LoadOnly" }
    if ($NoCache) { $args += "-NoCache" }
    Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "GHCR publish failed for ech-workers image"
}

Write-Host "Done publishing target: $Target" -ForegroundColor Green
