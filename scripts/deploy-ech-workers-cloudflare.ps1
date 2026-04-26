param(
    [switch]$DryRun
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")

Assert-EasyProxyCommand -Name "npx" -Hint "Install Node.js first."

$workerRoot = Resolve-EasyProxyPath -Path "workers/ech-workers-cloudflare"
$wranglerConfig = Join-Path $workerRoot "wrangler.jsonc"
Ensure-EasyProxyPathExists -Path $wranglerConfig -Message "Missing wrangler config: $wranglerConfig"

$args = @("--yes", "wrangler@4", "deploy", "--config", "wrangler.jsonc")
if ($DryRun) {
    $args += "--dry-run"
}

Write-Host "Deploying ech-workers-cloudflare via Wrangler..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "npx" -Arguments $args -WorkingDirectory $workerRoot -FailureMessage "ech-workers-cloudflare deploy failed"
Write-Host "ech-workers-cloudflare deploy finished."
