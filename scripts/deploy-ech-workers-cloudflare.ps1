param(
    [string]$TopologyPath = (Join-Path $PSScriptRoot '..\topology.yaml'),
    [switch]$DryRun,
    [switch]$SkipSecretSync
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-topology.ps1")

Assert-EasyProxyCommand -Name "npx" -Hint "Install Node.js first."

$topology = Read-EasyProxyTopology -TopologyPath $TopologyPath
if (-not [bool]$topology.components.ech_worker) {
    throw 'Topology does not enable components.ech_worker.'
}
$resourceNames = Get-EasyProxyResourceNames -TopologyPath $TopologyPath
$workerRoot = Resolve-EasyProxyPath -Path 'workers/ech-workers-cloudflare'
$wranglerConfig = Resolve-EasyProxyPath -Path 'workers/ech-workers-cloudflare/wrangler.jsonc'
Ensure-EasyProxyPathExists -Path $wranglerConfig -Message "Missing wrangler config: $wranglerConfig"

$previousAccountId = [Environment]::GetEnvironmentVariable('CLOUDFLARE_ACCOUNT_ID', 'Process')
$previousApiToken = [Environment]::GetEnvironmentVariable('CLOUDFLARE_API_TOKEN', 'Process')
try {
    $env:CLOUDFLARE_ACCOUNT_ID = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.cloudflare.account_id_env) -Purpose 'Cloudflare account selection'
    $env:CLOUDFLARE_API_TOKEN = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.cloudflare_api_token) -Purpose 'ECH Worker deployment'
    if (-not $DryRun -and -not $SkipSecretSync) {
        $echToken = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.ech_token) -Purpose 'ECH Worker authentication'
        Write-Host "Syncing Cloudflare secret ECH_TOKEN from the configured environment reference..." -ForegroundColor Cyan
        $echToken | npx --yes wrangler@4 secret put ECH_TOKEN --config $wranglerConfig --name ([string]$resourceNames.ech_worker)
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to sync Cloudflare secret ECH_TOKEN"
        }
    }

    $args = @("--yes", "wrangler@4", "deploy", "--config", "wrangler.jsonc", "--name", ([string]$resourceNames.ech_worker))
    if ($DryRun) {
        $args += "--dry-run"
    }

    Write-Host "Deploying ech-workers-cloudflare via Wrangler..." -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath "npx" -Arguments $args -WorkingDirectory $workerRoot -FailureMessage "ech-workers-cloudflare deploy failed"
}
finally {
    [Environment]::SetEnvironmentVariable('CLOUDFLARE_ACCOUNT_ID', $previousAccountId, 'Process')
    [Environment]::SetEnvironmentVariable('CLOUDFLARE_API_TOKEN', $previousApiToken, 'Process')
}
Write-Host "ech-workers-cloudflare deploy finished."
