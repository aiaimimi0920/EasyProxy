param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [string]$Repo = 'aiaimimi0920/EasyProxy',
    [string]$FallbackCloudflareConfig = 'C:\Users\Public\nas_home\AI\GameEditor\EasyEmail\config.yaml',
    [switch]$SkipGitHubSync
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

Assert-EasyProxyCommand -Name "python" -Hint "Install Python 3 first."

$scriptPath = Join-Path $PSScriptRoot 'sync-github-deployment-settings.py'
$args = @(
    $scriptPath,
    '--config-path', $ConfigPath,
    '--repo', $Repo,
    '--fallback-cloudflare-config', $FallbackCloudflareConfig
)
if ($SkipGitHubSync) {
    $args += '--skip-github-sync'
}

& python @args
if ($LASTEXITCODE -ne 0) {
    throw "Failed to sync GitHub deployment settings with exit code $LASTEXITCODE"
}
