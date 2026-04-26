param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [switch]$DryRun,
    [switch]$SkipRender,
    [switch]$SkipSecretSync
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-config.ps1")

Assert-EasyProxyCommand -Name "npx" -Hint "Install Node.js first."

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$worker = Get-EasyProxyConfigSection -Config $config -Name 'echWorkersCloudflare'
$workerRoot = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $worker -Name 'projectRoot' -Default 'workers/ech-workers-cloudflare')
$wranglerConfig = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $worker -Name 'wranglerConfig' -Default 'workers/ech-workers-cloudflare/wrangler.jsonc')
$devVarsOutput = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $worker -Name 'devVarsOutput' -Default 'workers/ech-workers-cloudflare/.dev.vars')
Ensure-EasyProxyPathExists -Path $wranglerConfig -Message "Missing wrangler config: $wranglerConfig"

if (-not $SkipRender) {
    $render = Join-Path $PSScriptRoot 'render-derived-configs.ps1'
    Invoke-EasyProxyExternalCommand -FilePath 'powershell' -Arguments @(
        '-ExecutionPolicy', 'Bypass',
        '-File', $render,
        '-ConfigPath', (Resolve-EasyProxyPath -Path $ConfigPath),
        '-EchWorkersCloudflare',
        '-WorkerDevVarsOutput', $devVarsOutput
    ) -FailureMessage "Failed to render worker .dev.vars from root config"
}

$secrets = Get-EasyProxyConfigSection -Config $worker -Name 'secrets'
$echToken = [string](Get-EasyProxyConfigValue -Object $secrets -Name 'ECH_TOKEN' -Default '')
if (-not $DryRun -and -not $SkipSecretSync -and -not [string]::IsNullOrWhiteSpace($echToken)) {
    Write-Host "Syncing Cloudflare secret ECH_TOKEN from root config..." -ForegroundColor Cyan
    $echToken | npx --yes wrangler@4 secret put ECH_TOKEN --config $wranglerConfig
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to sync Cloudflare secret ECH_TOKEN"
    }
}

$args = @("--yes", "wrangler@4", "deploy", "--config", "wrangler.jsonc")
if ($DryRun) {
    $args += "--dry-run"
}

Write-Host "Deploying ech-workers-cloudflare via Wrangler..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "npx" -Arguments $args -WorkingDirectory $workerRoot -FailureMessage "ech-workers-cloudflare deploy failed"
Write-Host "ech-workers-cloudflare deploy finished."
